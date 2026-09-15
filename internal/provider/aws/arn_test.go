package aws

import "testing"

func TestARNHelpers(t *testing.T) {
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"functionARN", functionARN("us-east-1", "123456789012", "myenv-api"),
			"arn:aws:lambda:us-east-1:123456789012:function:myenv-api"},
		{"roleARN", roleARN("123456789012", "myenv-api"),
			"arn:aws:iam::123456789012:role/myenv-api"},
		{"ruleARN", ruleARN("us-east-1", "123456789012", "myenv-tick"),
			"arn:aws:events:us-east-1:123456789012:rule/myenv-tick"},
		{"executeAPIArn", executeAPIArn("us-east-1", "123456789012", "abc123xyz"),
			"arn:aws:execute-api:us-east-1:123456789012:abc123xyz/*/*/*"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.got != c.want {
				t.Errorf("%s = %q, want %q", c.name, c.got, c.want)
			}
		})
	}
}
