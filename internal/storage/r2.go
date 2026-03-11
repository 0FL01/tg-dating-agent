package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
)

const defaultR2HTTPTimeout = 5 * time.Second

type R2Config struct {
	Bucket          string
	Endpoint        string
	Region          string
	AccessKeyID     string
	SecretAccessKey string
	InstanceName    string
	HTTPClient      *http.Client
}

type R2ObjectStore struct {
	bucket         string
	instancePrefix string
	client         s3API
}

type s3API interface {
	PutObject(ctx context.Context, params *s3.PutObjectInput, optFns ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	GetObject(ctx context.Context, params *s3.GetObjectInput, optFns ...func(*s3.Options)) (*s3.GetObjectOutput, error)
}

func NewR2ObjectStore(cfg R2Config) (*R2ObjectStore, error) {
	bucket := strings.TrimSpace(cfg.Bucket)
	if bucket == "" {
		return nil, fmt.Errorf("r2 bucket is required")
	}

	endpoint := strings.TrimSpace(cfg.Endpoint)
	if endpoint == "" {
		return nil, fmt.Errorf("r2 endpoint is required")
	}
	if _, err := url.ParseRequestURI(endpoint); err != nil {
		return nil, fmt.Errorf("invalid r2 endpoint %q: %w", endpoint, err)
	}

	region := strings.TrimSpace(cfg.Region)
	if region == "" {
		region = "auto"
	}

	accessKeyID := strings.TrimSpace(cfg.AccessKeyID)
	if accessKeyID == "" {
		return nil, fmt.Errorf("r2 access key id is required")
	}

	secretAccessKey := strings.TrimSpace(cfg.SecretAccessKey)
	if secretAccessKey == "" {
		return nil, fmt.Errorf("r2 secret access key is required")
	}

	instancePrefix := strings.TrimSpace(cfg.InstanceName)
	instancePrefix = strings.Trim(instancePrefix, "/")
	if instancePrefix == "" {
		return nil, fmt.Errorf("r2 instance name is required")
	}

	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultR2HTTPTimeout}
	}

	awsCfg := aws.Config{
		Region:      region,
		Credentials: aws.NewCredentialsCache(credentials.NewStaticCredentialsProvider(accessKeyID, secretAccessKey, "")),
		HTTPClient:  httpClient,
	}

	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
		o.UsePathStyle = true
	})

	return &R2ObjectStore{
		bucket:         bucket,
		instancePrefix: instancePrefix,
		client:         client,
	}, nil
}

func (s *R2ObjectStore) PutObject(ctx context.Context, key string, body io.Reader, contentType string) error {
	if s == nil {
		return fmt.Errorf("r2 object store is nil")
	}
	if body == nil {
		return fmt.Errorf("object body is nil")
	}

	scopedKey, err := s.scopedKey(key)
	if err != nil {
		return err
	}

	input := &s3.PutObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(scopedKey),
		Body:   body,
	}
	if strings.TrimSpace(contentType) != "" {
		input.ContentType = aws.String(contentType)
	}

	if _, err := s.client.PutObject(ctx, input); err != nil {
		return fmt.Errorf("put object %q: %w", key, err)
	}

	return nil
}

func (s *R2ObjectStore) GetObject(ctx context.Context, key string) (io.ReadCloser, error) {
	if s == nil {
		return nil, fmt.Errorf("r2 object store is nil")
	}

	scopedKey, err := s.scopedKey(key)
	if err != nil {
		return nil, err
	}

	output, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(scopedKey),
	})
	if err != nil {
		if isObjectNotFound(err) {
			return nil, fmt.Errorf("get object %q: %w", key, ErrObjectNotFound)
		}
		return nil, fmt.Errorf("get object %q: %w", key, err)
	}

	return output.Body, nil
}

func (s *R2ObjectStore) scopedKey(key string) (string, error) {
	trimmed := strings.TrimSpace(key)
	trimmed = strings.TrimPrefix(trimmed, "/")
	if trimmed == "" {
		return "", fmt.Errorf("object key is required")
	}

	return s.instancePrefix + "/" + trimmed, nil
}

func isObjectNotFound(err error) bool {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "NoSuchKey", "NotFound":
			return true
		}
	}

	return false
}
