package aws

import (
	"strings"
	"testing"
)

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
			"arn:aws:execute-api:us-east-1:123456789012:abc123xyz/*/*"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.got != c.want {
				t.Errorf("%s = %q, want %q", c.name, c.got, c.want)
			}
		})
	}
}

// TestExecuteAPIArnUsesTwoWildcards pins the segment count, which is the
// whole correctness question for this ARN and is not obvious from reading it.
//
// An HTTP API (v2) routing through a $default route invokes Lambda with
// {apiID}/{stage}/{route} — two segments after the api id. The three-wildcard
// form "/*/*/*" is the REST API (v1) stage/method/path shape; against an HTTP
// API it matches nothing, so API Gateway is silently denied permission.
//
// That failure is nearly undiagnosable from the outside, which is why this is
// pinned rather than left to the table above: every resource is created, the
// policy exists with the right principal, the route and integration are
// correct, and a direct Lambda invoke returns 200 — but every request through
// the gateway is a bare 500 and the function's log group records no
// invocation at all. Verified live: "/*/*/*" returned 500 with no Lambda
// logs; "/*/*" served 200 from the identical deployment.
func TestExecuteAPIArnUsesTwoWildcards(t *testing.T) {
	got := executeAPIArn("us-east-1", "409032463870", "mvfc6151jg")

	const want = "arn:aws:execute-api:us-east-1:409032463870:mvfc6151jg/*/*"
	if got != want {
		t.Errorf("executeAPIArn() = %q, want %q", got, want)
	}
	if strings.Count(got, "*") != 2 {
		t.Errorf("executeAPIArn() = %q: want exactly 2 wildcards (stage/route); "+
			"3 is the REST API shape and matches no HTTP API invoke", got)
	}
}
