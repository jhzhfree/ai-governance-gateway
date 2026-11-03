package main

import "testing"

func TestDevelopmentConfigFailsClosed(t *testing.T) {
	valid := config{database: "postgres://unused", issuer: "https://idp.example/realm", audience: "aigw-management", upstream: "http://127.0.0.1:8091/v1/chat/completions", listen: "127.0.0.1:8080", mockOnly: true}
	if err := valid.validate(); err != nil {
		t.Fatal(err)
	}
	for _, edit := range []func(*config){
		func(c *config) { c.mockOnly = false }, func(c *config) { c.database = "" },
		func(c *config) { c.issuer = "http://idp.example" }, func(c *config) { c.issuer = "https://user:password@idp.example" },
		func(c *config) { c.listen = "0.0.0.0:8080" }, func(c *config) { c.upstream = "http://169.254.169.254/v1/chat/completions" },
		func(c *config) { c.upstream = "http://localhost:8091/v1/chat/completions" }, func(c *config) { c.upstream = "http://127.0.0.1:8091/other" },
	} {
		c := valid
		edit(&c)
		if c.validate() == nil {
			t.Fatal("unsafe development configuration accepted")
		}
	}
}
