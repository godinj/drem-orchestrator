package store

import "testing"

func TestUserSvc_SaveLookup(t *testing.T) {
	u := NewUserSvc(&MemStore{})
	if err := u.Save("alice", "a@x.com"); err != nil {
		t.Fatal(err)
	}
	got, err := u.Lookup("alice")
	if err != nil {
		t.Fatal(err)
	}
	if got != "a@x.com" {
		t.Fatalf("got %q", got)
	}
}
