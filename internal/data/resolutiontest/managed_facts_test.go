package resolutiontest

import (
	"reflect"
	"testing"
)

func TestPrepareUsesPublishedTenantEnvironmentAndOwnsCopies(t *testing.T) {
	f := prepareResolution(t)
	facts, err := f.factReader.reader.ReadAdmissionFacts(f.ctx, f.request.IdentityRef.TenantID, f.request.Intent.ImplementationID)
	if err != nil || !reflect.DeepEqual(facts, f.facts) {
		t.Fatal("published environment facts changed", err)
	}
	if facts.Environment.BindingID == "" || facts.Environment.BindingDigest == "" || facts.Environment.Identities.TrainerServiceAccountUID == "" {
		t.Fatal("environment omitted original identity or binding digest")
	}
	facts.Environment.Identities.TrainerServiceAccount = "caller-change"
	facts.ManagedImageAccess.PullBindings[0].SecretName = "caller-change"
	again, err := f.factReader.reader.ReadAdmissionFacts(f.ctx, f.request.IdentityRef.TenantID, f.request.Intent.ImplementationID)
	if err != nil || !reflect.DeepEqual(again, f.facts) {
		t.Fatal("consumer mutated published facts", err)
	}
	requireNoResolvedExecution(t, f)
}
