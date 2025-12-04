package main

import "strings"

// matchPath checks if an actual HTTP path matches an expected pattern.
// Supports wildcards (*) in the expected pattern.
func matchPath(actual, expected string) bool {
	if expected == "*" {
		return true
	}

	if actual == expected {
		return true
	}

	if strings.Contains(expected, "*") {
		parts := strings.Split(expected, "*")

		if len(parts) == 2 {
			return strings.HasPrefix(actual, parts[0]) && strings.HasSuffix(actual, parts[1])
		}

		pos := 0
		for i, part := range parts {
			if part == "" {
				continue
			}

			idx := strings.Index(actual[pos:], part)
			if idx == -1 {
				return false
			}

			if i == 0 && idx != 0 {
				return false
			}

			pos += idx + len(part)
		}

		if parts[len(parts)-1] != "" {
			return strings.HasSuffix(actual, parts[len(parts)-1])
		}

		return true
	}

	return false
}
