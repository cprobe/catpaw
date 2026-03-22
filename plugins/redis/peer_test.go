package redis

import (
	"errors"
	"testing"
)

func TestClassifyConnError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"connection refused", errors.New("dial tcp 10.0.0.1:6379: connection refused"), "connection refused"},
		{"i/o timeout", errors.New("dial tcp 10.0.0.1:6379: i/o timeout"), "timeout"},
		{"deadline exceeded", errors.New("context deadline exceeded"), "timeout"},
		{"no route", errors.New("dial tcp 10.0.0.1:6379: no route to host"), "no route to host"},
		{"connection reset", errors.New("read tcp: connection reset by peer"), "connection reset"},
		{"NOAUTH", errors.New("NOAUTH Authentication required"), "auth failed"},
		{"AUTH wrong password", errors.New("ERR AUTH failed: invalid password"), "auth failed"},
		{"WRONGPASS", errors.New("WRONGPASS invalid username-password pair or user is disabled"), "auth failed"},
		{"context canceled", errors.New("context canceled"), "context cancelled"},
		{"unknown short", errors.New("something weird"), "something weird"},
		{"unknown long truncated", errors.New("a]very long error message that absolutely exceeds the eighty character limit and should be truncated"), "a]very long error message that absolutely exceeds the eighty character limit and..."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyConnError(tt.err)
			if got != tt.want {
				t.Errorf("classifyConnError(%q) = %q, want %q", tt.err, got, tt.want)
			}
		})
	}
}

func TestParseClusterNodes(t *testing.T) {
	raw := `aaa111 10.0.0.1:6379@16379 myself,master - 0 0 1 connected 0-5460
bbb222 10.0.0.2:6379@16379 master - 0 1234 2 connected 5461-10922
ccc333 10.0.0.3:6379@16379 slave bbb222 0 1234 0 connected
ddd444 :0@0 master,fail,noaddr - 0 0 3 disconnected 10923-16383
eee555 10.0.0.5:6379@16379 master,fail? - 0 1234 4 connected`

	nodes := parseClusterNodes(raw)
	if len(nodes) != 5 {
		t.Fatalf("expected 5 nodes, got %d", len(nodes))
	}

	self := nodes[0]
	if !self.IsSelf || self.Address != "10.0.0.1:6379" || self.Role != "master" {
		t.Errorf("self node parsed incorrectly: %+v", self)
	}

	noaddr := nodes[3]
	if !noaddr.IsNoAddr || !noaddr.IsFail || noaddr.Address != "" {
		t.Errorf("noaddr node parsed incorrectly: IsNoAddr=%v IsFail=%v Address=%q", noaddr.IsNoAddr, noaddr.IsFail, noaddr.Address)
	}

	pfail := nodes[4]
	if !pfail.IsPFail || pfail.Address != "10.0.0.5:6379" {
		t.Errorf("pfail node parsed incorrectly: %+v", pfail)
	}
}

func TestSelectProbeCandidates_NoaddrUnprobeable(t *testing.T) {
	nodes := []clusterNodeEntry{
		{ID: "aaa", Address: "10.0.0.1:6379", Role: "master", IsSelf: true},
		{ID: "bbb", Address: "10.0.0.2:6379", Role: "master"},
		{ID: "ccc", Address: "", Role: "master", IsFail: true, IsNoAddr: true, Flags: []string{"master", "fail", "noaddr"}},
		{ID: "ddd", Address: "10.0.0.4:6379", Role: "master", IsPFail: true},
	}

	probeable, unprobeable := selectProbeCandidates(nodes, "10.0.0.1:6379")

	if len(unprobeable) != 1 || unprobeable[0].ID != "ccc" {
		t.Errorf("expected noaddr node ccc in unprobeable, got %+v", unprobeable)
	}

	for _, p := range probeable {
		if p.IsNoAddr {
			t.Errorf("noaddr node %s should not be in probeable list", p.ID)
		}
		if p.IsSelf {
			t.Errorf("self node should not be in probeable list")
		}
	}

	if len(probeable) < 1 || probeable[0].ID != "ddd" {
		t.Errorf("pfail node ddd should be first in probeable (priority), got %+v", probeable)
	}
}
