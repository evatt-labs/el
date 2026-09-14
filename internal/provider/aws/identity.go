package aws

// cloudfrontMatch implements AWS::CloudFront::Distribution's LookupByAttr
// strategy (D26): AWS enforces alias (CNAME) uniqueness globally, so a
// distribution's Aliases is a safe attribute to search on.
//
// # Known gap: a distribution with no configured alias
//
// DistributionConfig.Aliases is optional — a distribution serving only its
// default *.cloudfront.net domain carries none. Such a distribution cannot
// be found by this strategy at all, since there is no alias for name to
// match against. D26 does not address this case for CloudFront specifically;
// it is surfaced here rather than silently accepted, and the write-path
// workstream needs an answer before it can create a distribution with no
// alias and expect a later Get to find it (a kraai-owned tag, the same
// mechanism ApiGatewayV2::Api uses below, is the likely fix).
func cloudfrontMatch(properties map[string]any, name string) bool {
	config, ok := properties["DistributionConfig"].(map[string]any)
	if !ok {
		return false
	}
	aliases, ok := config["Aliases"].([]any)
	if !ok {
		return false
	}
	for _, alias := range aliases {
		if s, ok := alias.(string); ok && s == name {
			return true
		}
	}
	return false
}

// identityTagKey is the kraai-owned tag byTag types are found by (D26).
//
// A byTag type's Create must set this tag in the create call itself, never
// as a follow-up write — a crash between the two would orphan the resource
// unfindably. Create is not implemented in this read-only slice, so nothing
// here writes the tag yet; it is named now because the write-path workstream
// must not invent a second name for the same concept.
const identityTagKey = "kraai:resource-name"

// apigatewayv2Match implements AWS::ApiGatewayV2::Api's LookupByTag strategy.
//
// # Why byTag rather than byName or byAttr
//
// D7's derivable-name assumption does not hold here the way it does for
// AWS::Lambda::Function or AWS::S3::Bucket: ApiGatewayV2::Api's Name is a
// plain, mutable string (CloudFormation's own reference marks it "Update
// requires: No interruption", i.e. not even a createOnlyProperty), and
// neither the CreateApi nor the CloudFormation resource documentation
// declares it unique — the API reference's own worked examples return
// multiple Api objects distinguished only by apiId, and CreateApi's 409
// ConflictException is documented as "the resource already exists" with no
// stated connection to Name. D26 requires "an attribute the provider
// guarantees unique" for byAttr; Name here gives no such guarantee, which is
// exactly the ACM::Certificate case D26 already names — a kraai-owned tag,
// not a provider attribute, is the safe strategy.
//
// Tags for this type is CloudFormation's "Object of String" shape — a flat
// map, unlike S3 or CloudFront's Tags: [{Key, Value}, ...] array shape — so
// no array-walking is needed here.
func apigatewayv2Match(properties map[string]any, name string) bool {
	tags, ok := properties["Tags"].(map[string]any)
	if !ok {
		return false
	}
	value, ok := tags[identityTagKey].(string)
	return ok && value == name
}
