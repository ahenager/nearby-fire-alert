package providerapi

import (
	"context"
	"testing"
	"time"
)

type fakeProvider struct {
	name   string
	conf   map[string]any
	gotGet map[string]string
}

func (f *fakeProvider) Name() string { return f.name }
func (f *fakeProvider) Configure(cfg map[string]any, get func(string) (string, bool)) error {
	f.conf = cfg
	f.gotGet = map[string]string{}
	// Probe a known key.
	if v, ok := get("TEST_KEY"); ok {
		f.gotGet["TEST_KEY"] = v
	}
	return nil
}
func (f *fakeProvider) Fetch(context.Context, time.Time) ([]Event, error) { return nil, nil }
func (f *fakeProvider) Enabled() bool                                     { return true }

func TestRegistryRegisterBuild(t *testing.T) {
	r := NewRegistry()
	r.Register(&fakeProvider{name: "fake"})
	if names := r.Names(); len(names) != 1 || names[0] != "fake" {
		t.Fatalf("bad names: %v", names)
	}
	get := func(k string) (string, bool) {
		if k == "TEST_KEY" {
			return "value", true
		}
		return "", false
	}
	p, err := r.Build("fake", map[string]any{"sensors": "a,b"}, get)
	if err != nil {
		t.Fatal(err)
	}
	f := p.(*fakeProvider)
	if f.gotGet["TEST_KEY"] != "value" {
		t.Fatalf("get not passed through: %v", f.gotGet)
	}
	if _, ok := f.conf["sensors"]; !ok {
		t.Fatalf("cfg not passed: %v", f.conf)
	}
}

func TestRegistryBuildUnknown(t *testing.T) {
	r := NewRegistry()
	r.Register(&fakeProvider{name: "fake"})
	if _, err := r.Build("missing", nil, func(string) (string, bool) { return "", false }); err == nil {
		t.Fatal("expected error for unknown provider")
	}
}

func TestRegistryRegisterDuplicatePanics(t *testing.T) {
	r := NewRegistry()
	r.Register(&fakeProvider{name: "fake"})
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on duplicate registration")
		}
	}()
	r.Register(&fakeProvider{name: "fake"})
}
