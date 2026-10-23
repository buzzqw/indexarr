package cardigann

import (
	"fmt"
	"sort"
	"strings"

	"github.com/buzzqw/indexarr/internal/definition"
)

// convertSettings maps the declared Cardigann settings and records the
// original-name -> native-slug mapping used by templates.
func (c *converter) convertSettings() {
	for _, raw := range c.raw.Settings {
		original := strings.TrimSpace(raw.Name)
		if original == "" {
			c.addWarning("setting without a name ignored")
			continue
		}
		if _, exists := c.settingNames[original]; exists {
			c.addWarning("duplicate setting %q ignored", original)
			continue
		}

		setting := definition.Setting{
			Label:    strings.TrimSpace(raw.Label),
			Help:     strings.TrimSpace(raw.Help),
			Default:  strings.TrimSpace(string(raw.Default)),
			Required: raw.Required,
			Options:  append([]string(nil), raw.Options...),
		}
		setting.Type = c.normaliseSettingType(original, raw.Type, len(setting.Options) > 0)
		if setting.Label == "" {
			setting.Label = original
		}

		secret := isSecretName(original) || strings.EqualFold(strings.TrimSpace(raw.Type), "password")
		if secret {
			if setting.Default != "" {
				// A default in a local definition may be a real token or
				// password. Keep only the setting name and let the user enter
				// the value in the vault-backed form.
				c.addSecret(setting.Default)
				c.addWarning("default value for secret setting %q was not copied", original)
				setting.Default = ""
			}
			setting.Type = "password"
		}
		if strings.Contains(setting.Default, "{{") {
			c.addWarning("default value for setting %q is a template and was not copied", original)
			setting.Default = ""
		}

		c.addSetting(original, setting, false)
	}
}

func (c *converter) normaliseSettingType(name, raw string, hasOptions bool) string {
	kind := strings.ToLower(strings.TrimSpace(raw))
	if kind == "" {
		if hasOptions {
			return "select"
		}
		return "text"
	}
	switch kind {
	case "text", "string", "url", "email":
		if hasOptions {
			return "select"
		}
		return "text"
	case "password", "pass":
		return "password"
	case "checkbox", "bool", "boolean":
		return "checkbox"
	case "select", "choice", "dropdown":
		return "select"
	case "radio":
		c.addWarning("setting %q type radio converted to select", name)
		return "select"
	case "number", "int", "integer":
		return "number"
	case "info":
		c.addWarning("setting %q type info converted to text", name)
		return "text"
	default:
		c.addWarning("setting %q has unknown type %q; using text", name, raw)
		return "text"
	}
}

// addSetting inserts a setting once. original is the Cardigann name; the
// native name is a slug, made unique within this definition.
func (c *converter) addSetting(original string, setting definition.Setting, forceRequired bool) string {
	if existing, ok := c.settingNames[original]; ok {
		if forceRequired {
			c.markRequired(existing)
		}
		return existing
	}

	native := slugify(original)
	if native == "" {
		native = "setting"
	}
	base := native
	for suffix := 2; c.usedSettings[native]; suffix++ {
		native = fmt.Sprintf("%s-%d", base, suffix)
	}

	setting.Name = native
	if setting.Type == "" {
		setting.Type = "text"
	}
	if setting.Label == "" {
		setting.Label = original
	}
	if isSecretName(original) {
		setting.Type = "password"
		setting.Default = ""
	}
	if setting.Type == "password" {
		setting.Default = ""
	}
	if forceRequired {
		setting.Required = true
	}

	c.settingNames[original] = native
	c.usedSettings[native] = true
	c.settings = append(c.settings, setting)
	return native
}

func (c *converter) markRequired(native string) {
	for i := range c.settings {
		if c.settings[i].Name == native {
			c.settings[i].Required = true
			return
		}
	}
}

// ensureSetting returns the native name for a template's .Config reference,
// adding a setting when the source definition forgot to declare it.
func (c *converter) ensureSetting(original string, required bool) string {
	original = strings.TrimSpace(original)
	if original == "" {
		return ""
	}
	if native, ok := c.lookupSetting(original); ok {
		if required {
			c.markRequired(native)
		}
		return native
	}
	c.addWarning("setting %q was referenced but not declared; added", original)
	setting := definition.Setting{
		Label: original,
		Type:  "text",
	}
	if isSecretName(original) {
		setting.Type = "password"
	}
	return c.addSetting(original, setting, required)
}

func (c *converter) lookupSetting(original string) (string, bool) {
	if native, ok := c.settingNames[original]; ok {
		return native, true
	}
	keys := make([]string, 0, len(c.settingNames))
	for key := range c.settingNames {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if strings.EqualFold(key, original) {
			return c.settingNames[key], true
		}
	}
	return "", false
}

func (c *converter) addSecret(value string) {
	value = strings.TrimSpace(value)
	// Short values are still never copied, but they are too common to use as
	// redaction needles without mangling unrelated warnings.
	if len(value) < 8 {
		return
	}
	if !containsString(c.secrets, value) {
		c.secrets = append(c.secrets, value)
	}
}
