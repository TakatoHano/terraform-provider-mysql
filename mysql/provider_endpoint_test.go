package mysql

import "testing"

func TestLocalPortFromEndpoint(t *testing.T) {
	cases := []struct {
		name     string
		endpoint string
		want     uint16
		wantErr  bool
	}{
		{"localhost", "localhost:3307", 3307, false},
		{"ipv4", "127.0.0.1:13306", 13306, false},
		{"unix socket", "/var/run/mysqld.sock", 0, true},
		{"no port", "db.example.com", 0, true},
		{"non numeric port", "localhost:abc", 0, true},
		{"port out of range", "localhost:70000", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := localPortFromEndpoint(tc.endpoint)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("got %d, want %d", got, tc.want)
			}
		})
	}
}
