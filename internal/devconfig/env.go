package devconfig

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
)

var keyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Load reads the local Compose .env file without executing it. Shell variables
// take precedence. DATABASE_URL in .env is ignored: the local URL is built from
// POSTGRES_USER, POSTGRES_PASSWORD and PG_PORT instead.
func Load() error {
	return loadFile(".env")
}

func loadFile(path string) error {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return errors.New("cannot read .env")
	}
	defer f.Close()
	values, err := Parse(f)
	if err != nil {
		return err
	}
	for key, value := range values {
		if key == "DATABASE_URL" {
			continue
		}
		if _, exists := os.LookupEnv(key); !exists {
			if err := os.Setenv(key, value); err != nil {
				return fmt.Errorf("set %s: %w", key, err)
			}
		}
	}
	return nil
}

// Parse supports the one-line key=value syntax shared by Compose and this
// project. Single quotes preserve dollar signs literally. Unsupported
// interpolation is rejected so Compose and Go cannot silently disagree.
func Parse(r io.Reader) (map[string]string, error) {
	values := make(map[string]string)
	scanner := bufio.NewScanner(io.LimitReader(r, 1<<20))
	for line := 1; scanner.Scan(); line++ {
		entry := strings.TrimSpace(scanner.Text())
		if entry == "" || strings.HasPrefix(entry, "#") {
			continue
		}
		key, value, ok := strings.Cut(entry, "=")
		key = strings.TrimSpace(key)
		if !ok || !keyPattern.MatchString(key) {
			return nil, fmt.Errorf("invalid .env entry at line %d", line)
		}
		parsed, err := parseValue(strings.TrimSpace(value))
		if err != nil {
			return nil, fmt.Errorf("invalid .env value for %s at line %d: %w", key, line, err)
		}
		values[key] = parsed
	}
	if err := scanner.Err(); err != nil {
		return nil, errors.New("cannot read .env")
	}
	return values, nil
}

func parseValue(value string) (string, error) {
	if strings.HasPrefix(value, "'") {
		var b strings.Builder
		for i := 1; i < len(value); i++ {
			if value[i] == '\\' && i+1 < len(value) && (value[i+1] == '\'' || value[i+1] == '\\') {
				i++
				b.WriteByte(value[i])
				continue
			}
			if value[i] == '\'' {
				if !commentOrEmpty(value[i+1:]) {
					return "", errors.New("text after closing quote")
				}
				return b.String(), nil
			}
			b.WriteByte(value[i])
		}
		return "", errors.New("unclosed quote")
	}
	if strings.HasPrefix(value, `"`) {
		closing := strings.LastIndex(value, `"`)
		if closing == 0 || !commentOrEmpty(value[closing+1:]) {
			return "", errors.New("invalid quoted value")
		}
		parsed, err := strconv.Unquote(value[:closing+1])
		if err != nil {
			return "", errors.New("invalid quoted value")
		}
		value = parsed
	} else if i := strings.Index(value, " #"); i >= 0 {
		value = strings.TrimSpace(value[:i])
	}
	if strings.Contains(value, "$") {
		return "", errors.New("use single quotes for a literal dollar sign")
	}
	return value, nil
}

func commentOrEmpty(tail string) bool {
	tail = strings.TrimSpace(tail)
	return tail == "" || strings.HasPrefix(tail, "#")
}

// DatabaseURL uses an explicit environment URL for external databases and CI.
// For local development it constructs a correctly escaped loopback URL.
func DatabaseURL() (string, error) {
	if value := os.Getenv("DATABASE_URL"); value != "" {
		return value, nil
	}
	password := os.Getenv("POSTGRES_PASSWORD")
	if password == "" {
		return "", errors.New("set POSTGRES_PASSWORD in .env (copy .env.example first)")
	}
	user := os.Getenv("POSTGRES_USER")
	if user == "" {
		user = "scigraph"
	}
	port := os.Getenv("PG_PORT")
	if port == "" {
		port = "5432"
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return "", errors.New("PG_PORT must be between 1 and 65535")
	}
	u := url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(user, password),
		Host:   net.JoinHostPort("127.0.0.1", port),
		Path:   "/scigraph",
	}
	u.RawQuery = "sslmode=disable"
	return u.String(), nil
}
