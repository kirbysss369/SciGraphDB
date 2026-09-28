package devconfig

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseComposeSubset(t *testing.T) {
	values, err := Parse(strings.NewReader("# local settings\nPOSTGRES_USER=scigraph\nPOSTGRES_PASSWORD='a $b # c'\nPG_PORT=55432 # alternate port\nOPENALEX_API_KEY=key#part\nHTTP_ADDR=\"127.0.0.1:8080\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"POSTGRES_USER": "scigraph", "POSTGRES_PASSWORD": "a $b # c",
		"PG_PORT": "55432", "OPENALEX_API_KEY": "key#part", "HTTP_ADDR": "127.0.0.1:8080",
	} {
		if values[key] != want {
			t.Errorf("%s: got %q, want %q", key, values[key], want)
		}
	}
}

func TestParseRejectsAmbiguousValuesWithoutSecret(t *testing.T) {
	for _, input := range []string{"POSTGRES_PASSWORD=$OTHER", "POSTGRES_PASSWORD='unfinished", "INVALID-KEY=value"} {
		if _, err := Parse(strings.NewReader(input)); err == nil || strings.Contains(err.Error(), "$OTHER") {
			t.Fatalf("expected safe parse error for %q: %v", input, err)
		}
	}
}

func TestLoadFilePrecedenceAndOldURL(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("POSTGRES_PASSWORD=from-file\nDATABASE_URL=postgres://old:secret@localhost/old\nOPENALEX_API_KEY=local-key\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("POSTGRES_PASSWORD", "from-shell")
	t.Setenv("DATABASE_URL", "")
	keyBefore, keyPresent := os.LookupEnv("OPENALEX_API_KEY")
	if err := os.Unsetenv("OPENALEX_API_KEY"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if keyPresent {
			_ = os.Setenv("OPENALEX_API_KEY", keyBefore)
		} else {
			_ = os.Unsetenv("OPENALEX_API_KEY")
		}
	})
	if err := loadFile(path); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("POSTGRES_PASSWORD") != "from-shell" || os.Getenv("OPENALEX_API_KEY") != "local-key" || os.Getenv("DATABASE_URL") != "" {
		t.Fatal("shell override or legacy DATABASE_URL handling failed")
	}
}

func TestDatabaseURL(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	t.Setenv("POSTGRES_USER", "scigraph")
	t.Setenv("POSTGRES_PASSWORD", "a@b:c/# $d")
	t.Setenv("PG_PORT", "55432")
	dsn, err := DatabaseURL()
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	password, ok := u.User.Password()
	if !ok || password != "a@b:c/# $d" || u.Host != "127.0.0.1:55432" || u.Path != "/scigraph" || u.Query().Get("sslmode") != "disable" {
		t.Fatalf("incorrect derived connection URL: user=%q host=%q path=%q", u.User.Username(), u.Host, u.Path)
	}
	t.Setenv("DATABASE_URL", "postgres://remote/test")
	if explicit, err := DatabaseURL(); err != nil || explicit != "postgres://remote/test" {
		t.Fatalf("explicit URL was not retained: %v", err)
	}
}

func TestInvalidLocalDatabaseConfig(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	t.Setenv("POSTGRES_PASSWORD", "")
	if _, err := DatabaseURL(); err == nil {
		t.Fatal("missing password accepted")
	}
	t.Setenv("POSTGRES_PASSWORD", "present")
	t.Setenv("PG_PORT", "invalid")
	if _, err := DatabaseURL(); err == nil {
		t.Fatal("invalid port accepted")
	}
}
