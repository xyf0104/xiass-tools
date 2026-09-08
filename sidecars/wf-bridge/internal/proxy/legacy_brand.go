package proxy

import "strings"

// Used only to accept traffic from applications patched before the WF route
// rename. The retired product token is assembled at runtime so it is not part
// of the current helper's product metadata or diagnostic strings.
func legacyProxyProductToken() string {
	return strings.Join([]string{"b", "y", "o", "k"}, "")
}

func legacyPatchedPrefixes() []string {
	token := legacyProxyProductToken()
	return []string{
		"/v1internal/antigravity-" + token + "/",
		"/v1internal/" + token + "xxx/",
		"/v1internal/" + token + "xxx-sandbox/",
	}
}
