package device

import (
	"testing"

	"github.com/gopcua/opcua/ua"

	"github.com/labnote/labnote-device-connector/internal/model"
)

func ep(policy string, mode ua.MessageSecurityMode, token ua.UserTokenType) *ua.EndpointDescription {
	return &ua.EndpointDescription{
		SecurityPolicyURI:  "http://opcfoundation.org/UA/SecurityPolicy#" + policy,
		SecurityMode:       mode,
		UserIdentityTokens: []*ua.UserTokenPolicy{{TokenType: token}},
	}
}

func TestSelectSecureEndpointPrefersStrongestEncryption(t *testing.T) {
	endpoints := []*ua.EndpointDescription{
		ep("None", ua.MessageSecurityModeNone, ua.UserTokenTypeCertificate),
		ep("Basic256Sha256", ua.MessageSecurityModeSignAndEncrypt, ua.UserTokenTypeCertificate),
		ep("Aes256_Sha256_RsaPss", ua.MessageSecurityModeSignAndEncrypt, ua.UserTokenTypeCertificate),
	}
	got := selectSecureEndpoint(endpoints, model.SecurityPolicyAuto, ua.UserTokenTypeCertificate, false)
	if got == nil || policyName(got.SecurityPolicyURI) != model.SecurityPolicyAes256Sha256RsaPss {
		t.Fatalf("expected the strongest policy, got %v", got)
	}
}

func TestSelectSecureEndpointWorksWithoutBasic256Sha256(t *testing.T) {
	endpoints := []*ua.EndpointDescription{
		ep("Aes128_Sha256_RsaOaep", ua.MessageSecurityModeSignAndEncrypt, ua.UserTokenTypeUserName),
	}
	if selectSecureEndpoint(endpoints, model.SecurityPolicyAuto, ua.UserTokenTypeUserName, false) == nil {
		t.Fatal("an instrument offering only Aes128 must still be usable")
	}
	if selectSecureEndpoint(endpoints, model.SecurityPolicyBasic256Sha256, ua.UserTokenTypeUserName, false) != nil {
		t.Fatal("a pinned policy must not be silently substituted")
	}
}

func TestSelectSecureEndpointRefusesInsecureAndSignOnlyByDefault(t *testing.T) {
	insecure := []*ua.EndpointDescription{
		ep("None", ua.MessageSecurityModeNone, ua.UserTokenTypeAnonymous),
		ep("Basic128Rsa15", ua.MessageSecurityModeSignAndEncrypt, ua.UserTokenTypeCertificate),
	}
	if selectSecureEndpoint(insecure, model.SecurityPolicyAuto, ua.UserTokenTypeCertificate, false) != nil {
		t.Fatal("unencrypted and deprecated policies must be refused")
	}

	signOnly := []*ua.EndpointDescription{
		ep("Basic256Sha256", ua.MessageSecurityModeSign, ua.UserTokenTypeCertificate),
	}
	if selectSecureEndpoint(signOnly, model.SecurityPolicyAuto, ua.UserTokenTypeCertificate, false) != nil {
		t.Fatal("sign-only must require the explicit opt-in")
	}
	if !hasSignOnly(signOnly, ua.UserTokenTypeCertificate) {
		t.Fatal("hasSignOnly should detect the sign-only offer so the UI can explain it")
	}
	if selectSecureEndpoint(signOnly, model.SecurityPolicyAuto, ua.UserTokenTypeCertificate, true) == nil {
		t.Fatal("sign-only must be used once allowed")
	}
}

func TestSelectSecureEndpointHonoursLoginType(t *testing.T) {
	endpoints := []*ua.EndpointDescription{
		ep("Basic256Sha256", ua.MessageSecurityModeSignAndEncrypt, ua.UserTokenTypeCertificate),
	}
	if selectSecureEndpoint(endpoints, model.SecurityPolicyAuto, ua.UserTokenTypeUserName, false) != nil {
		t.Fatal("an endpoint that rejects the configured login must not be chosen")
	}
}
