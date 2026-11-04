package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// credentialFile is the mounted Secret shape the S3 client reads on each call.
// Rotating the Secret therefore takes effect without restarting the job, and the
// value never enters the process environment or the plan.
type credentialFile struct {
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
	SessionToken    string `json:"session_token"`
}

func newS3Client(endpoint, region, credentialPath string) (*s3.Client, error) {
	if endpoint == "" || region == "" || credentialPath == "" {
		return nil, errors.New("s3 endpoint, region and credential file are required")
	}
	provider := aws.CredentialsProviderFunc(func(ctx context.Context) (aws.Credentials, error) {
		if ctx == nil || ctx.Err() != nil {
			return aws.Credentials{}, ctx.Err()
		}
		raw, err := os.ReadFile(credentialPath)
		if err != nil {
			return aws.Credentials{}, err
		}
		var credential credentialFile
		if err := json.Unmarshal(raw, &credential); err != nil {
			return aws.Credentials{}, err
		}
		if credential.AccessKeyID == "" || credential.SecretAccessKey == "" ||
			strings.ContainsAny(credential.AccessKeyID+credential.SecretAccessKey+credential.SessionToken, " \t\r\n") {
			return aws.Credentials{}, errors.New("invalid credential file")
		}
		return aws.Credentials{
			AccessKeyID: credential.AccessKeyID, SecretAccessKey: credential.SecretAccessKey,
			SessionToken: credential.SessionToken, Source: "ani-dataset-issuance",
		}, nil
	})
	return s3.New(s3.Options{
		Region: region, BaseEndpoint: aws.String(endpoint), UsePathStyle: true,
		Credentials: provider,
	}), nil
}
