package sqlstore

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/tavora-vtt/tavora-server/internal/storage"
)

var segmentPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

type Dialect interface {
	Name() string
	Rebind(query string) string
	JSONArg() string
	JSONColumn(column string) string
	JSONSet(column string, path []string) string
	JSONRemove(column string, paths [][]string) string
	JSONText(column string, path []string) string
	JSONNumber(column string, path []string) string
	JSONType(column string, path []string) string
	JSONBool(column string, path []string) string
	JSONArrayContains(column string, path []string) string
	TimeArg(value time.Time) any
	TimePtrArg(value *time.Time) any
	Migrations() [][]string
	IsUniqueViolation(err error) bool
}

func splitPath(path string) ([]string, error) {
	if path == "" {
		return nil, fmt.Errorf("%w: empty", storage.ErrInvalidPath)
	}
	segments := strings.Split(path, ".")
	for _, segment := range segments {
		if !segmentPattern.MatchString(segment) {
			return nil, fmt.Errorf("%w: %q", storage.ErrInvalidPath, path)
		}
	}
	return segments, nil
}

func splitPaths(paths []string) ([][]string, error) {
	out := make([][]string, 0, len(paths))
	for _, path := range paths {
		segments, err := splitPath(path)
		if err != nil {
			return nil, err
		}
		out = append(out, segments)
	}
	return out, nil
}

func RebindDollar(query string) string {
	var builder strings.Builder
	builder.Grow(len(query) + 8)

	index := 0
	for i := 0; i < len(query); i++ {
		if query[i] != '?' {
			builder.WriteByte(query[i])
			continue
		}
		index++
		builder.WriteByte('$')
		builder.WriteString(fmt.Sprint(index))
	}
	return builder.String()
}
