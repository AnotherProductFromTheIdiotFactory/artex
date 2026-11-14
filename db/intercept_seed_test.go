package db

import (
	"regexp"
	"testing"
)

// The built-in destructive-endpoint rule matches the complete tool_input JSON,
// so these cases use the same JSON-shaped subject that Interceptor receives.
func TestDeleteEndpointPathPattern(t *testing.T) {
	re := regexp.MustCompile(deleteEndpointPathPattern)

	hit := []string{
		`{"command":"curl -s 'http://t.com/api/user/delete?id=1'"}`,    // GET request to a destructive endpoint
		`{"command":"curl -X POST http://t.com/admin/delete -d id=1"}`, // POST request to a destructive endpoint
		`{"command":"curl 'http://t.com/api/deleteAll'"}`,
		`{"command":"curl 'http://t.com/api/delete_user?id=1'"}`,
		`{"command":"curl 'http://t.com/api/delete-user?id=1'"}`,
		`{"url":"http://t.com/api/remove?id=1"}`,
		`{"command":"curl http://t.com/files/unlink/3"}`,
		`{"command":"curl http://t.com/api/del?id=2"}`,
		`{"command":"curl -X POST http://t/v1/erase"}`,
		`{"command":"curl http://t/admin/destroyAll"}`, // v1 did not permit suffixes; this rule closes that gap
	}
	for _, s := range hit {
		if !re.MatchString(s) {
			t.Errorf("expected a match but the input was allowed: %s", s)
		}
	}

	// Require a separator after the verb to avoid blocking read-only paths such
	// as /delivery and /details.
	miss := []string{
		`{"command":"curl 'http://t.com/api/delivery?id=1'"}`,
		`{"command":"curl 'http://t.com/order/details'"}`,
		`{"command":"curl 'http://t.com/api/delta/sync'"}`,
		`{"command":"curl 'http://t.com/user/delegate'"}`,
		`{"command":"curl 'http://delete.example.com/'"}`, // destructive verb appears in the hostname, not the path
		`{"command":"curl 'http://t.com/remote/status'"}`,
		`{"command":"nmap -p80 10.0.0.1"}`,
	}
	for _, s := range miss {
		if re.MatchString(s) {
			t.Errorf("unexpected block: %s", s)
		}
	}
}
