package jara

import (
	"os"
	"testing"
)

func TestHostsLearnTheNameYouCallThemBy(t *testing.T) {
	t.Setenv("JARA_DATA", t.TempDir())
	rememberHostName("jellyfish")
	rememberHostName("bad name") // not a host name: ignored
	if got := loadHostName(); got.Name != "jellyfish" || got.Manual {
		t.Fatalf("learned %+v", got)
	}
	rememberHostName("jelly") // the latest client's name wins
	if got := loadHostName().Name; got != "jelly" {
		t.Fatalf("latest %q", got)
	}
	// A name set by hand stays until auto.
	if err := setHostName("work-box"); err != nil {
		t.Fatal(err)
	}
	rememberHostName("jellyfish")
	if got := loadHostName(); got.Name != "work-box" || !got.Manual {
		t.Fatalf("manual name replaced: %+v", got)
	}
	if err := setHostName("auto"); err != nil {
		t.Fatal(err)
	}
	rememberHostName("jellyfish")
	if got := loadHostName(); got.Name != "jellyfish" || got.Manual {
		t.Fatalf("after auto %+v", got)
	}
	if err := setHostName("no spaces please"); err == nil {
		t.Fatal("an invalid name was accepted")
	}
}

func TestControlsNameTheHostWindowsRunOn(t *testing.T) {
	t.Setenv("JARA_DATA", t.TempDir())
	saved := chatHostName
	t.Cleanup(func() { chatHostName, chatPlace = saved, chatPlacement{} })
	chatHostName = func() string { return loadHostName().Name }
	rememberHostName("jellyfish")
	// A window running on the host itself, as after ssh and jara connect.
	chatPlace = chatPlacement{}
	if got := chatControlHost(); got != "jellyfish" {
		t.Fatalf("host window %q", got)
	}
	// Client windows know their place: here shows nothing, a host its name.
	chatPlace = chatPlacement{root: "/folder", session: "s1"}
	if got := chatControlHost(); got != "" {
		t.Fatalf("local window %q", got)
	}
	chatPlace.remote = "work"
	if got := chatControlHost(); got != "work" {
		t.Fatalf("remote window %q", got)
	}
}

func TestSetupNamesTheHost(t *testing.T) {
	t.Setenv("JARA_DATA", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	// jara setup, remote add and doctor run the host's doctor check with
	// the name they saved it as.
	stdout := os.Stdout
	devnull, _ := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	os.Stdout = devnull
	err := remoteRPC(Request{Op: "doctor", RemoteName: "jellyfish"})
	os.Stdout = stdout
	devnull.Close()
	if err != nil {
		t.Fatal(err)
	}
	if got := loadHostName().Name; got != "jellyfish" {
		t.Fatalf("doctor left the host unnamed: %q", got)
	}
}
