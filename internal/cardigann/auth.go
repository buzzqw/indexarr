package cardigann

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/buzzqw/indexarr/internal/definition"
	"gopkg.in/yaml.v3"
)

// convertAuth maps a Cardigann form login to explicit native auth steps:
// optionally a GET on the login page to extract hidden CSRF/token inputs, then
// a POST with the form. If the flow cannot be represented safely the result is
// no auth at all plus a warning: the importer never emits an auth block that
// looks complete but silently skips a captcha or a custom step.
func (c *converter) convertAuth() *definition.Auth {
	login := c.raw.Login
	if login == nil {
		return nil
	}

	if nodeSet(login.Captcha) || nodeSet(login.Recaptcha) || nodeSet(login.TwoFactor) || nodeSet(login.TwoFactorAlt) {
		c.addUnsupported("captcha")
		return c.noAuth("login uses a captcha or two-factor step, which native v1 cannot automate")
	}
	if nodeSet(login.Go) || nodeSet(login.Custom) || nodeSet(login.Code) || nodeSet(login.Script) {
		c.addUnsupported("custom Go")
		return c.noAuth("login uses custom code, which is never executed or translated")
	}
	if strings.TrimSpace(login.Path) == "" {
		return c.noAuth("login has no path")
	}
	method := strings.ToLower(strings.TrimSpace(login.Method))
	switch method {
	case "", "post", "form":
		// Default Cardigann method is POST.
	case "get":
		c.addUnsupported("login method get")
		return c.noAuth("GET logins cannot be represented as a native form POST")
	default:
		c.addUnsupported("login method " + method)
		return c.noAuth("login method " + method + " is not a form POST")
	}
	if len(login.Inputs) == 0 {
		c.addUnsupported("login without form inputs")
		return c.noAuth("login has no form inputs")
	}
	if strings.TrimSpace(login.Form) != "" {
		c.addWarning("login.form selector %q ignored; native auth posts to login.path", login.Form)
	}
	if nodeSet(login.Headers) {
		c.addUnsupported("login.headers")
		c.addWarning("login.headers are not mapped; login may fail if the site requires them")
	}

	loginPath, err := c.convertTemplate("login.path", login.Path, true)
	if err != nil {
		return c.noAuth("login path cannot be converted")
	}

	form := make(map[string]string, len(login.Inputs))
	extract := make(map[string]definition.Extractor)
	for _, inputName := range sortedKeys(login.Inputs) {
		value := login.Inputs[inputName]
		refs, onlyConfig := configReferences(value)
		switch {
		case onlyConfig && containsCSRF(refs) && !singleTemplateExpression(value):
			c.addUnsupported("login token with static text")
			return c.noAuth("a login token input mixes a token reference with static text")
		case onlyConfig && len(refs) == 1 && (isCSRFField(inputName) || isCSRFField(refs[0])):
			if !safeExtractName(inputName) {
				c.addUnsupported("login input " + inputName)
				return c.noAuth("a CSRF input name cannot be used as a native template variable")
			}
			extract[inputName] = definition.Extractor{
				Selector: "input[name=\"" + escapeCSSAttr(inputName) + "\"]",
				Attr:     "value",
			}
			form[inputName] = "{{ " + inputName + " }}"
			c.extracted[inputName] = true
		case onlyConfig && len(refs) > 1 && containsCSRF(refs):
			c.addUnsupported("multi-field login token")
			return c.noAuth("one login input references more than one token")
		case onlyConfig:
			for _, ref := range refs {
				c.ensureSetting(ref, true)
			}
			converted, err := c.convertTemplate("login.inputs."+inputName, value, true)
			if err != nil {
				return c.noAuth("login input " + inputName + " cannot be converted")
			}
			form[inputName] = converted
		case strings.Contains(value, "{{"):
			converted, err := c.convertTemplate("login.inputs."+inputName, value, true)
			if err != nil {
				return c.noAuth("login input " + inputName + " uses an unsupported template")
			}
			form[inputName] = converted
		case isCSRFLiteralField(inputName):
			// A literal CSRF token is single-use and must come from the page.
			c.addSecret(value)
			if !safeExtractName(inputName) {
				c.addUnsupported("login input " + inputName)
				return c.noAuth("a CSRF input name cannot be used as a native template variable")
			}
			extract[inputName] = definition.Extractor{
				Selector: "input[name=\"" + escapeCSSAttr(inputName) + "\"]",
				Attr:     "value",
			}
			form[inputName] = "{{ " + inputName + " }}"
			c.extracted[inputName] = true
			c.addWarning("literal value for login input %q was not copied; it is extracted from the login page", inputName)
		case isSecretName(inputName) || isLoginCredentialName(inputName):
			// Never copy a literal credential from the source definition.
			if isSecretName(inputName) {
				c.addSecret(value)
			}
			native := c.ensureSetting(inputName, true)
			form[inputName] = "{{ settings." + native + " }}"
			c.addWarning("literal credential in login input %q was replaced by a setting reference", inputName)
		default:
			form[inputName] = value
		}
	}

	var steps []definition.AuthStep
	if len(extract) > 0 {
		steps = append(steps, definition.AuthStep{
			Get:     loginPath,
			Extract: extract,
		})
	}
	post := definition.AuthStep{Post: loginPath, Form: form}
	if header := c.loginCookieHeader(login); header != "" {
		post.Headers = map[string]string{"Cookie": header}
	}
	steps = append(steps, post)

	auth := &definition.Auth{
		Strategy: "form",
		Steps:    steps,
	}

	if login.Test != nil {
		if path := strings.TrimSpace(login.Test.Path); path != "" {
			convertedPath, err := c.convertTemplate("login.test.path", path, false)
			if err != nil {
				c.addWarning("login.test.path cannot be converted; success falls back to the login response")
			} else if strings.TrimSpace(convertedPath) != strings.TrimSpace(loginPath) {
				auth.Steps = append(auth.Steps, definition.AuthStep{Get: convertedPath})
			}
		}
		if strings.TrimSpace(login.Test.Selector) != "" || login.Test.Status != 0 {
			auth.Success = &definition.Condition{
				Selector: strings.TrimSpace(login.Test.Selector),
				Message:  strings.TrimSpace(login.Test.Message),
				Status:   login.Test.Status,
			}
		} else {
			c.addWarning("login.test has no selector or status; success is any 2xx/3xx response")
		}
	} else {
		c.addWarning("login.test is missing; native auth treats any 2xx/3xx login response as success")
	}

	for _, condition := range login.Error {
		if path := strings.TrimSpace(condition.Path); path != "" && path != strings.TrimSpace(login.Path) {
			c.addUnsupported("login error condition on a different path")
			c.addWarning("login.error condition for %q ignored", sanitizeURL(path))
			continue
		}
		if strings.TrimSpace(condition.Selector) == "" && condition.Status == 0 {
			c.addWarning("login.error condition without selector or status ignored")
			continue
		}
		auth.Failure = append(auth.Failure, definition.Condition{
			Selector: strings.TrimSpace(condition.Selector),
			Message:  strings.TrimSpace(condition.Message),
			Status:   condition.Status,
		})
	}

	if refresh := strings.TrimSpace(firstNonEmpty(login.RefreshAfter, login.Refresh)); refresh != "" {
		if duration, err := time.ParseDuration(refresh); err == nil && duration > 0 {
			auth.RefreshAfter = definition.Duration(duration)
		} else if seconds, err := strconv.Atoi(refresh); err == nil && seconds > 0 {
			auth.RefreshAfter = definition.Duration(time.Duration(seconds) * time.Second)
		} else {
			c.addWarning("login refresh value %q is not a duration; using the engine default", refresh)
		}
	}

	return auth
}

func (c *converter) noAuth(reason string) *definition.Auth {
	c.addWarning("authentication not converted: %s; the definition has no auth", reason)
	return nil
}

// configReferences returns the .Config.<name> references in a template value
// and whether every expression is such a reference.
func configReferences(value string) ([]string, bool) {
	matches := templatePattern.FindAllStringSubmatch(value, -1)
	if len(matches) == 0 {
		return nil, false
	}
	refs := make([]string, 0, len(matches))
	for _, match := range matches {
		expr := normaliseExpression(match[1])
		if strings.Contains(expr, "|") || !strings.HasPrefix(expr, ".") {
			return nil, false
		}
		field := strings.TrimPrefix(expr, ".")
		if !strings.HasPrefix(strings.ToLower(field), "config.") {
			return nil, false
		}
		key := strings.TrimSpace(field[len("config."):])
		if key == "" {
			return nil, false
		}
		refs = append(refs, key)
	}
	return refs, true
}

func containsCSRF(refs []string) bool {
	for _, ref := range refs {
		if isCSRFField(ref) {
			return true
		}
	}
	return false
}

func singleTemplateExpression(value string) bool {
	trimmed := strings.TrimSpace(value)
	matches := templatePattern.FindAllStringSubmatch(trimmed, -1)
	return len(matches) == 1 && matches[0][0] == trimmed
}

func safeExtractName(name string) bool {
	if name == "" {
		return false
	}
	lower := strings.ToLower(strings.TrimSpace(name))
	switch lower {
	case "categories", "page", "offset", "limit":
		return false
	}
	if strings.HasPrefix(lower, "settings.") || strings.HasPrefix(lower, "query.") {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '_', r == '-', r == '.':
		default:
			return false
		}
	}
	return true
}

// loginCookieHeader converts login.cookies mappings into an explicit Cookie
// header. Cookie values are never copied literally: a value becomes a
// referenced setting so the secret stays in the vault-backed form.
func (c *converter) loginCookieHeader(login *rawLogin) string {
	if login == nil || !nodeSet(login.Cookies) {
		return ""
	}
	node := login.Cookies
	if node.Kind == 0 {
		return ""
	}
	if node.Kind == yaml.AliasNode && node.Alias != nil {
		node = *node.Alias
	}
	if node.Kind != yaml.MappingNode {
		c.addWarning("login.cookies is not a mapping; native session handling captures cookies automatically")
		return ""
	}

	pairs := make([]string, 0, len(node.Content)/2)
	for i := 0; i+1 < len(node.Content); i += 2 {
		name := scalarText(node.Content[i])
		if name == "" {
			continue
		}
		rawValue := nodeText(node.Content[i+1])
		value := rawValue
		if strings.Contains(rawValue, "{{") {
			converted, err := c.convertTemplate("login.cookies."+name, rawValue, false)
			if err != nil {
				c.addWarning("cookie %q was not converted", name)
				continue
			}
			value = converted
		} else {
			// A literal cookie is a session credential. Reference a setting
			// instead of embedding it in the definition.
			c.addSecret(rawValue)
			native := c.ensureSetting("cookie_"+name, false)
			value = "{{ settings." + native + " }}"
			c.addWarning("literal value for cookie %q was not copied; it is a setting reference", name)
		}
		pairs = append(pairs, name+"="+value)
	}
	if len(pairs) == 0 {
		return ""
	}
	sort.Strings(pairs)
	return strings.Join(pairs, "; ")
}
