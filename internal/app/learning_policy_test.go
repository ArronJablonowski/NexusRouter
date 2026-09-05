package app

import "testing"

func TestLearningPolicyBindsUnredactedConfigurationNotCredentials(t *testing.T) {
	svc, _ := autoFixture(t)
	svc.settings.Providers[0].APIKeyEnv = "LEARNING_TEST_KEY"
	secret := "fixture-credential-not-in-configuration"
	svc.secret = func(string) string { return secret }
	before, err := svc.learningPolicy()
	if err != nil {
		t.Fatal(err)
	}
	secret = "rotated-fixture-credential-not-in-configuration"
	if after, err := svc.learningPolicy(); err != nil || after != before {
		t.Fatal("credential rotation changed scheduling identity", err)
	}
	endpoint := svc.settings.Providers[0].Endpoint
	svc.settings.Providers[0].Endpoint = "http://127.0.0.1:12345"
	if after, err := svc.learningPolicy(); err != nil || after == before {
		t.Fatal("redacted endpoint failed to bind policy", err)
	}
	svc.settings.Providers[0].Endpoint = endpoint
	svc.settings.Skills.Root = "/private/changed-catalog"
	if after, err := svc.learningPolicy(); err != nil || after == before {
		t.Fatal("redacted catalog path failed to bind policy", err)
	}
	secret = "changed-catalog"
	if _, err := svc.learningPolicy(); err == nil {
		t.Fatal("known credential in configuration was hashed")
	}
}
