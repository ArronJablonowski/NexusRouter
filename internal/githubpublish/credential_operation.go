package githubpublish

import "context"

type CredentialPublicationConfig struct {
	APIBase    string
	UploadBase string
	Transport  RoundTripper
	Secrets    SecretSource
}

// PublishCredentialedRelease is the sole production constructor for a
// credential-bearing publisher. The private credential lease is created only
// after the complete plan and endpoints validate, used for exactly one full
// publication operation, and always closed before return.
func PublishCredentialedRelease(ctx context.Context, config CredentialPublicationConfig, input PublicationPlan) (PublicationEvidence, error) {
	var empty PublicationEvidence
	plan, err := validatePublicationPlan(input)
	if err != nil || ctx == nil || ctx.Err() != nil || config.Transport == nil || config.Secrets == nil {
		return empty, ErrCredentialTransport
	}
	if _, err = endpoint(config.APIBase, "api.github.com"); err != nil {
		return empty, ErrCredentialTransport
	}
	if _, err = endpoint(config.UploadBase, "uploads.github.com"); err != nil {
		return empty, ErrCredentialTransport
	}
	lease, err := newCredentialTransport(ctx, config.Transport, config.Secrets, plan.Owner+"/"+plan.Repository)
	if err != nil {
		return empty, ErrCredentialTransport
	}
	defer lease.Close()
	publisher, err := New(Config{APIBase: config.APIBase, UploadBase: config.UploadBase, Transport: lease})
	if err != nil {
		return empty, ErrCredentialTransport
	}
	return publisher.PublishRelease(ctx, plan)
}
