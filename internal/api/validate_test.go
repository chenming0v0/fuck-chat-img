package api

import (
	"net/url"
	"testing"

	"github.com/fuck-chat-img/fci/internal/model"
)

func TestValidateUpstreamModelWeightDefault(t *testing.T) {
	m := &model.UpstreamModel{Weight: 0, MaxRetries: 0}
	err := validateUpstreamModel(m, "test")
	if err != nil {
		t.Fatalf("零 Weight/MaxRetries 不应报错, err=%v", err)
	}
	if m.Weight != 1 {
		t.Errorf("Weight 未设置时默认应为 1, 实际 %d", m.Weight)
	}
	if m.MaxRetries != 1 {
		t.Errorf("MaxRetries 未设置时默认应为 1, 实际 %d", m.MaxRetries)
	}
}

func TestValidateUpstreamModelNegativeWeight(t *testing.T) {
	m := &model.UpstreamModel{Weight: -1}
	err := validateUpstreamModel(m, "test")
	if err == nil {
		t.Error("负 Weight 应报错")
	}
}

func TestValidateUpstreamModelExplicitWeightPreserved(t *testing.T) {
	m := &model.UpstreamModel{Weight: 5, MaxRetries: 3}
	err := validateUpstreamModel(m, "test")
	if err != nil {
		t.Fatal(err)
	}
	if m.Weight != 5 {
		t.Errorf("显式设置的 Weight 应保留, 期望 5, 实际 %d", m.Weight)
	}
	if m.MaxRetries != 3 {
		t.Errorf("显式设置的 MaxRetries 应保留, 期望 3, 实际 %d", m.MaxRetries)
	}
}

func TestHostsMatch(t *testing.T) {
	cases := []struct {
		origin     string
		request    string
		requestTLS bool
		want       bool
	}{
		{"http://localhost", "localhost", false, true},
		{"http://localhost:8080", "localhost:8080", false, true},
		{"https://example.com", "example.com", true, true},
		{"http://example.com", "other.com", false, false},
		// 跨 scheme 应判为不同源: Origin 是 https 但请求是 http
		{"https://localhost", "localhost", false, false},
	}
	for _, c := range cases {
		u, err := url.Parse(c.origin)
		if err != nil {
			t.Fatalf("url.Parse(%q) error: %v", c.origin, err)
		}
		got := hostsMatch(u, c.request, c.requestTLS)
		if got != c.want {
			t.Errorf("hostsMatch(%q,%q,tls=%v)=%v, 期望 %v", c.origin, c.request, c.requestTLS, got, c.want)
		}
	}
}
