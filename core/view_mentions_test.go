package core

import "testing"

func TestViewMentions(t *testing.T) {
	cases := []struct {
		q, name string
		want    bool
	}{
		{"SELECT id, status FROM orders", "status", false},
		{"SELECT id FROM orders", "orders", true},
		{"SELECT a.id FROM `users` a JOIN orders o ON 1=1", "users", true},
		{"select * from users_public", "users", false},
		{"SELECT 'users' AS k FROM orders", "users", false},
		{"SELECT x FROM (SELECT id FROM users)", "users", true},
		{"SELECT o.id FROM orders o, customers c WHERE o.c = c.id", "customers", true},
		{"SELECT id FROM public.customers", "customers", true},
		{"SELECT id FROM \"public\".\"customers\"", "customers", true},
	}
	for _, c := range cases {
		if got := viewMentions(c.q, c.name); got != c.want {
			t.Errorf("viewMentions(%q, %q) = %v, want %v", c.q, c.name, got, c.want)
		}
	}
}
