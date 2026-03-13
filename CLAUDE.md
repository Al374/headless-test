# AnyCompany Users Service

## Project Overview

Serverless Users Service for AnyCompany. A RESTful API for managing user records with
JWT-based authentication. All infrastructure is defined with Terraform and deployed to AWS.

## Technology Stack

- **IaC**: Terraform (`.tf` files in project root)
- **Language**: Python 3.10
- **Compute**: AWS Lambda
- **Database**: Amazon DynamoDB (on-demand billing)
- **API**: Amazon API Gateway REST API (v1)
- **Auth**: Amazon Cognito User Pool + Lambda Token Authorizer

## Best Practices

- Use descriptive resource names with a consistent prefix to avoid naming collisions
- Lambda functions use Python logging (`logging.getLogger()`) at INFO level
- All HTTP responses include CORS headers
- Timestamps stored as ISO 8601 strings
- User IDs are UUIDs generated server-side when not provided by the caller
- Terraform resources are organized by concern (one `.tf` file per logical component)
- API Gateway is defined using an OpenAPI 3.0 JSON body for clear endpoint documentation

## Testing

- Unit tests live in `tests/unit/` and run with `pytest`
- Use `moto` for AWS service mocking in unit tests
- Every Lambda function should have test coverage for its happy path and error cases