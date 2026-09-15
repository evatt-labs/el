package aws

import "fmt"

// This file centralizes the ARN shapes this package constructs locally
// (never via a live cross-resource lookup — see client.go's AccountID doc
// comment for why): one function per AWS resource shape, each a pure
// string format with no I/O of its own. Every caller still needs
// Client.AccountID resolved first, which is where the one real network
// call in this chain lives.

// functionARN builds a Lambda function's ARN from its derived name. Used
// wherever another resource's desired state needs to reference the
// function by full ARN rather than the bare name AWS::Lambda::Url and
// AWS::Lambda::Permission's FunctionName both accept directly (see
// lambdaurl.go and lambdapermission.go for why those two skip this
// entirely).
func functionARN(region, account, name string) string {
	return fmt.Sprintf("arn:aws:lambda:%s:%s:function:%s", region, account, name)
}

// roleARN builds an IAM role's ARN from its derived name. IAM ARNs carry
// no region segment — roles are account-scoped, not regional.
func roleARN(account, name string) string {
	return fmt.Sprintf("arn:aws:iam::%s:role/%s", account, name)
}

// ruleARN builds an EventBridge rule's ARN on the account's default event
// bus, from its derived name. kraai never creates rules on a custom event
// bus (register.go's Events::Rule registration sets no EventBusName), so
// the default-bus ARN shape is the only one this package ever needs.
func ruleARN(region, account, name string) string {
	return fmt.Sprintf("arn:aws:events:%s:%s:rule/%s", region, account, name)
}

// executeAPIArn builds an API Gateway HTTP API's execute-api ARN, scoped to
// every stage/method/resource path via the standard wildcard suffix —
// apiID cannot be derived locally (it is AWS-assigned, not a name kraai
// chooses), so this always requires whatever live lookup produced it; see
// lambdapermission.go's apiGatewaySourceARN.
func executeAPIArn(region, account, apiID string) string {
	return fmt.Sprintf("arn:aws:execute-api:%s:%s:%s/*/*/*", region, account, apiID)
}
