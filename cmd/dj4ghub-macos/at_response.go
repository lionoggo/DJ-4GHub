package main

import "strings"

func atResponseComplete(resp string) bool {
	normalized := strings.ToUpper(strings.ReplaceAll(resp, "\r\n", "\n"))
	return strings.Contains(normalized, "\nOK\n") ||
		strings.HasSuffix(normalized, "\nOK") ||
		strings.Contains(normalized, "\nCONNECT\n") ||
		strings.HasSuffix(normalized, "\nCONNECT") ||
		strings.Contains(normalized, "\nNO CARRIER\n") ||
		strings.HasSuffix(normalized, "\nNO CARRIER") ||
		strings.Contains(normalized, "\nBUSY\n") ||
		strings.HasSuffix(normalized, "\nBUSY") ||
		strings.Contains(normalized, "\nNO ANSWER\n") ||
		strings.HasSuffix(normalized, "\nNO ANSWER") ||
		atResponseIsError(normalized)
}

func atResponseIsError(resp string) bool {
	normalized := strings.ToUpper(strings.ReplaceAll(resp, "\r\n", "\n"))
	return strings.Contains(normalized, "\nERROR\n") ||
		strings.HasSuffix(normalized, "\nERROR") ||
		strings.Contains(normalized, "+CME ERROR:") ||
		strings.Contains(normalized, "+CMS ERROR:")
}

func atResponseHasPrompt(resp string) bool {
	trimmed := strings.TrimRight(resp, " \t\r\n")
	return strings.HasSuffix(trimmed, ">")
}

// A probe must receive OK. ERROR merely proves that a bulk interface accepted
// bytes; it is not the modem's AT channel (the QMI interface can do that).
func atProbeSucceeded(resp string) bool {
	normalized := strings.ReplaceAll(strings.TrimSpace(resp), "\r\n", "\n")
	return normalized == "OK" || strings.HasSuffix(normalized, "\nOK")
}

func normalizeATResponse(resp string) string {
	resp = strings.ReplaceAll(resp, "\r\r\n", "\r\n")
	resp = strings.TrimSpace(resp)
	lines := strings.Split(resp, "\n")
	filtered := lines[:0]
	for _, line := range lines {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		filtered = append(filtered, line)
	}
	return strings.Join(filtered, "\r\n")
}
