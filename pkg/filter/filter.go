package filter

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/gobwas/glob"
)

type Filter interface {
	Match(string) bool
}

// isRegexPattern checks if a pattern uses /regex/ syntax.
func isRegexPattern(s string) bool {
	return len(s) >= 2 && s[0] == '/' && s[len(s)-1] == '/'
}

// Compile takes a list of string filters and returns a Filter interface
// for matching a given string against the filter list (OR logic).
//
// Patterns surrounded by / are treated as regular expressions:
//
//	f, _ := Compile([]string{"*error*", "/(?i)panic|segfault/"})
//	f.Match("an error occurred")    // true (glob match)
//	f.Match("KERNEL PANIC")         // true (regex match)
//	f.Match("normal log")           // false
//
// Plain patterns support glob matching:
//
//	f, _ := Compile([]string{"cpu", "mem", "net*"})
//	f.Match("cpu")     // true
//	f.Match("network") // true
//	f.Match("memory")  // false
func Compile(filters []string) (Filter, error) {
	if len(filters) == 0 {
		return nil, nil
	}

	var globPatterns []string
	var regexFilters []Filter

	for _, p := range filters {
		if isRegexPattern(p) {
			re, err := regexp.Compile(p[1 : len(p)-1])
			if err != nil {
				return nil, fmt.Errorf("invalid regex %q: %v", p, err)
			}
			regexFilters = append(regexFilters, &regexFilter{patterns: []*regexp.Regexp{re}})
		} else {
			globPatterns = append(globPatterns, p)
		}
	}

	var parts []Filter

	if len(globPatterns) > 0 {
		gf, err := compileGlob(globPatterns)
		if err != nil {
			return nil, err
		}
		parts = append(parts, gf)
	}

	parts = append(parts, regexFilters...)

	if len(parts) == 0 {
		return nil, nil
	}
	if len(parts) == 1 {
		return parts[0], nil
	}
	return &CombinedFilter{filters: parts}, nil
}

// compileGlob compiles a list of pure glob/literal patterns (no /regex/ syntax).
func compileGlob(filters []string) (Filter, error) {
	noGlob := true
	for _, f := range filters {
		if HasMeta(f) {
			noGlob = false
			break
		}
	}

	switch {
	case noGlob:
		return compileFilterNoGlob(filters), nil
	case len(filters) == 1:
		return glob.Compile(filters[0])
	default:
		return glob.Compile("{" + strings.Join(filters, ",") + "}")
	}
}

// HasMeta reports whether path contains any magic glob characters.
func HasMeta(s string) bool {
	return strings.ContainsAny(s, "*?[")
}

type filter struct {
	m map[string]struct{}
}

func (f *filter) Match(s string) bool {
	_, ok := f.m[s]
	return ok
}

type filtersingle struct {
	s string
}

func (f *filtersingle) Match(s string) bool {
	return f.s == s
}

func compileFilterNoGlob(filters []string) Filter {
	if len(filters) == 1 {
		return &filtersingle{s: filters[0]}
	}
	out := filter{m: make(map[string]struct{})}
	for _, filter := range filters {
		out.m[filter] = struct{}{}
	}
	return &out
}

type IncludeExcludeFilter struct {
	include        Filter
	exclude        Filter
	includeDefault bool
	excludeDefault bool
}

func NewIncludeExcludeFilter(
	include []string,
	exclude []string,
) (Filter, error) {
	return NewIncludeExcludeFilterDefaults(include, exclude, true, false)
}

func NewIncludeExcludeFilterDefaults(
	include []string,
	exclude []string,
	includeDefault bool,
	excludeDefault bool,
) (Filter, error) {
	in, err := Compile(include)
	if err != nil {
		return nil, err
	}

	ex, err := Compile(exclude)
	if err != nil {
		return nil, err
	}

	return &IncludeExcludeFilter{in, ex, includeDefault, excludeDefault}, nil
}

func (f *IncludeExcludeFilter) Match(s string) bool {
	if f.include != nil {
		if !f.include.Match(s) {
			return false
		}
	} else if !f.includeDefault {
		return false
	}

	if f.exclude != nil {
		if f.exclude.Match(s) {
			return false
		}
	} else if f.excludeDefault {
		return false
	}

	return true
}

// regexFilter matches if any of the compiled regexps match.
type regexFilter struct {
	patterns []*regexp.Regexp
}

func (f *regexFilter) Match(s string) bool {
	for _, re := range f.patterns {
		if re.MatchString(s) {
			return true
		}
	}
	return false
}

// CombinedFilter matches if ANY of its sub-filters match (OR logic).
type CombinedFilter struct {
	filters []Filter
}

func (f *CombinedFilter) Match(s string) bool {
	for _, sub := range f.filters {
		if sub.Match(s) {
			return true
		}
	}
	return false
}
