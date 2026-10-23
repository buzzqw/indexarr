package definition

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestUpgradeCurrent(t *testing.T) {
	body := []byte("schema: 1\nmeta:\n  id: current\n")
	out, version, err := Upgrade(body)
	if err != nil {
		t.Fatalf("Upgrade: %v", err)
	}
	if version != CurrentSchema {
		t.Errorf("version = %d, want %d", version, CurrentSchema)
	}
	if !bytes.Contains(out, []byte("schema: 1")) {
		t.Errorf("canonical output does not declare schema 1:\n%s", out)
	}
}

func TestUpgradeMissingSchemaDefaultsToCurrent(t *testing.T) {
	body := []byte("meta:\n  id: missing-schema\n")
	out, version, err := Upgrade(body)
	if err != nil {
		t.Fatalf("Upgrade: %v", err)
	}
	if version != CurrentSchema {
		t.Errorf("version = %d, want %d", version, CurrentSchema)
	}
	if !bytes.Contains(out, []byte("schema: 1")) {
		t.Errorf("canonical output does not declare schema 1:\n%s", out)
	}
}

func TestUpgradeFutureSchema(t *testing.T) {
	_, version, err := Upgrade([]byte("schema: 99\n"))
	if err == nil {
		t.Fatal("expected an error for a future schema")
	}
	if !errors.Is(err, ErrFutureSchema) {
		t.Errorf("error = %v, want ErrFutureSchema", err)
	}
	if version != 99 {
		t.Errorf("version = %d, want 99", version)
	}
}

func TestUpgradeInvalidSchemaValue(t *testing.T) {
	if _, _, err := Upgrade([]byte("schema: one\n")); err == nil {
		t.Fatal("expected an error for a non-numeric schema")
	}
}

func TestUpgradeSyntheticV0(t *testing.T) {
	upgrade := func(in map[string]any) (map[string]any, error) {
		id, _ := in["id"].(string)
		name, _ := in["name"].(string)
		in["meta"] = map[string]any{
			"id":       id,
			"name":     name,
			"type":     "public",
			"protocol": "torrent",
			"urls":     []any{"https://legacy.example/"},
		}
		// Connector-based definitions may omit search.
		in["connector"] = "native"
		delete(in, "id")
		delete(in, "name")
		return in, nil
	}

	if err := RegisterUpgrader(0, upgrade); err != nil && !strings.Contains(err.Error(), "already registered") {
		t.Fatalf("RegisterUpgrader: %v", err)
	}
	if err := RegisterUpgrader(0, upgrade); err == nil {
		t.Fatal("expected duplicate registration to fail")
	}

	body := []byte("schema: 0\nid: legacy\nname: Legacy\n")
	out, version, err := Upgrade(body)
	if err != nil {
		t.Fatalf("Upgrade: %v", err)
	}
	if version != CurrentSchema {
		t.Errorf("version = %d, want %d", version, CurrentSchema)
	}
	if bytes.Contains(out, []byte("name: Legacy\nid: legacy")) {
		t.Errorf("old fields were not migrated:\n%s", out)
	}

	def, err := Parse(out)
	if err != nil {
		t.Fatalf("upgraded definition does not parse: %v\n%s", err, out)
	}
	if def.Meta.ID != "legacy" || def.Meta.Name != "Legacy" {
		t.Errorf("unexpected upgraded meta: %+v", def.Meta)
	}
	if def.Connector != "native" {
		t.Errorf("connector = %q, want native", def.Connector)
	}

	again, _, err := Upgrade(body)
	if err != nil {
		t.Fatalf("second Upgrade: %v", err)
	}
	if !bytes.Equal(out, again) {
		t.Errorf("upgrade is not deterministic:\nfirst:\n%s\nsecond:\n%s", out, again)
	}
}

func TestRegisterUpgraderValidation(t *testing.T) {
	if err := RegisterUpgrader(-1, func(map[string]any) (map[string]any, error) { return nil, nil }); err == nil {
		t.Error("expected negative version to fail")
	}
	if err := RegisterUpgrader(CurrentSchema, func(map[string]any) (map[string]any, error) { return nil, nil }); err == nil {
		t.Error("expected current version to be rejected")
	}
	if err := RegisterUpgrader(0, nil); err == nil {
		t.Error("expected nil upgrader to fail")
	}
}
