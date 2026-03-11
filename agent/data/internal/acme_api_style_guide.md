# ACME Corp REST API Style Guide

## Table of Contents
1. [Overview](#overview)
2. [General Principles](#general-principles)
3. [URL Structure](#url-structure)
4. [HTTP Methods](#http-methods)
5. [Request Format](#request-format)
6. [Response Format](#response-format)
7. [Status Codes](#status-codes)
8. [Error Handling](#error-handling)
9. [Authentication](#authentication)
10. [Versioning](#versioning)
11. [Pagination](#pagination)
12. [Rate Limiting](#rate-limiting)
13. [Documentation](#documentation)
14. [Testing](#testing)

## Overview

This style guide establishes consistent standards for all REST APIs developed at ACME Corp. Following these guidelines ensures our APIs are intuitive, maintainable, and provide excellent developer experience.

## General Principles

### Design Philosophy
- **Developer-First**: APIs should be intuitive and easy to use
- **Consistency**: Maintain consistent patterns across all endpoints
- **Simplicity**: Prefer simple, clear solutions over complex ones
- **Backward Compatibility**: Changes should not break existing integrations
- **Security by Design**: Security considerations should be built-in, not bolted-on

### Naming Conventions
- Use clear, descriptive names that reflect business domain
- Prefer full words over abbreviations
- Use American English spelling consistently
- Follow existing industry standards where applicable

## URL Structure

### Base URL Format
```
https://api.acme.com/v1/
```

### Resource Naming
- Use **plural nouns** for collections: `/users`, `/products`, `/orders`
- Use **lowercase** with **hyphens** for multi-word resources: `/user-profiles`, `/order-items`
- Avoid file extensions in URLs: ❌ `/users.json` ✅ `/users`

### Hierarchy and Nesting
- Limit nesting to 2 levels maximum
- Use sub-resources for related data:
  ```
  GET /users/123/orders
  GET /orders/456/items
  ```

### Query Parameters
- Use **snake_case** for parameter names
- Use descriptive parameter names:
  ```
  GET /products?category=electronics&price_min=100&price_max=500
  GET /users?created_after=2024-01-01&status=active
  ```

### Examples
```
✅ Good
GET /users
GET /users/123
GET /users/123/orders
GET /products?category=electronics
POST /users
PUT /users/123
DELETE /users/123

❌ Bad
GET /getUsers
GET /user/123
GET /users/123/orders/456/items/789/details
GET /products?cat=elec
```

## HTTP Methods

### Standard Usage
- **GET**: Retrieve resources (safe, idempotent)
- **POST**: Create new resources or perform actions
- **PUT**: Update entire resources (idempotent)
- **PATCH**: Partial updates to resources
- **DELETE**: Remove resources (idempotent)

### Method-Specific Guidelines

#### GET Requests
- Must be safe (no side effects)
- Should be cacheable
- Use query parameters for filtering, sorting, pagination

#### POST Requests
- Use for creating resources
- Use for non-idempotent operations
- Return `201 Created` with location header for resource creation

#### PUT Requests
- Must be idempotent
- Should replace the entire resource
- Include all required fields in request body

#### PATCH Requests
- Use for partial updates
- Should be idempotent when possible
- Include only fields being updated

#### DELETE Requests
- Must be idempotent
- Return `204 No Content` for successful deletion
- Return `404 Not Found` if resource doesn't exist

## Request Format

### Content Type
- Use `application/json` for request and response bodies
- Set `Content-Type: application/json` header
- Use `multipart/form-data` only for file uploads

### Request Body Structure
```json
{
  "user": {
    "first_name": "John",
    "last_name": "Doe",
    "email": "john.doe@example.com",
    "preferences": {
      "newsletter": true,
      "notifications": false
    }
  }
}
```

### Field Naming
- Use **snake_case** for JSON field names
- Use descriptive field names
- Avoid abbreviations unless widely understood

### Data Types
- Use appropriate JSON data types:
  - Strings for text: `"name": "John Doe"`
  - Numbers for numeric values: `"price": 29.99`
  - Booleans for true/false: `"active": true`
  - Arrays for lists: `"tags": ["electronics", "mobile"]`
  - Objects for complex data: `"address": {"street": "123 Main St"}`

## Response Format

### Success Response Structure
```json
{
  "data": {
    "id": "user_123",
    "type": "user",
    "attributes": {
      "first_name": "John",
      "last_name": "Doe",
      "email": "john.doe@example.com",
      "created_at": "2024-01-15T10:30:00Z",
      "updated_at": "2024-01-15T10:30:00Z"
    }
  }
}
```

### Collection Response Structure
```json
{
  "data": [
    {
      "id": "user_123",
      "type": "user",
      "attributes": {
        "first_name": "John",
        "last_name": "Doe",
        "email": "john.doe@example.com"
      }
    }
  ],
  "meta": {
    "total": 150,
    "page": 1,
    "per_page": 25,
    "total_pages": 6
  }
}
```

### Field Guidelines
- Always include `id` for resources
- Use ISO 8601 format for dates: `"2024-01-15T10:30:00Z"`
- Include `created_at` and `updated_at` for mutable resources
- Use consistent field naming across all endpoints

## Status Codes

### Success Codes
- **200 OK**: Successful GET, PUT, PATCH, or DELETE
- **201 Created**: Successful POST that creates a resource
- **204 No Content**: Successful DELETE or PUT with no response body

### Client Error Codes
- **400 Bad Request**: Invalid request syntax or parameters
- **401 Unauthorized**: Missing or invalid authentication
- **403 Forbidden**: Authenticated but insufficient permissions
- **404 Not Found**: Resource doesn't exist
- **409 Conflict**: Resource conflict (e.g., duplicate email)
- **422 Unprocessable Entity**: Valid syntax but semantic errors
- **429 Too Many Requests**: Rate limit exceeded

### Server Error Codes
- **500 Internal Server Error**: Generic server error
- **502 Bad Gateway**: Invalid response from upstream server
- **503 Service Unavailable**: Server temporarily unavailable

## Error Handling

### Error Response Format
```json
{
  "error": {
    "code": "validation_failed",
    "message": "The request could not be processed due to validation errors",
    "details": [
      {
        "field": "email",
        "code": "invalid_format",
        "message": "Email address is not valid"
      },
      {
        "field": "age",
        "code": "out_of_range",
        "message": "Age must be between 13 and 120"
      }
    ],
    "request_id": "req_123456789"
  }
}
```

### Error Guidelines
- Always include a human-readable error message
- Provide specific error codes for programmatic handling
- Include field-level errors for validation failures
- Add request ID for debugging and support
- Never expose internal system details in error messages

### Common Error Codes
- `invalid_request`: Malformed request
- `authentication_failed`: Invalid credentials
- `permission_denied`: Insufficient permissions
- `resource_not_found`: Resource doesn't exist
- `validation_failed`: Input validation errors
- `rate_limit_exceeded`: Too many requests

## Authentication

### API Key Authentication
```http
GET /users
Authorization: Bearer sk_live_abc123def456
```

### JWT Authentication
```http
GET /users
Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...
```

### Guidelines
- Use HTTPS for all API endpoints
- Include authentication in `Authorization` header
- Implement proper token expiration and refresh mechanisms
- Return `401 Unauthorized` for invalid authentication
- Return `403 Forbidden` for insufficient permissions

## Versioning

### URL Versioning
```
https://api.acme.com/v1/users
https://api.acme.com/v2/users
```

### Guidelines
- Use major version numbers in URL path
- Maintain backward compatibility within major versions
- Provide migration guides for major version changes
- Support previous major version for at least 12 months
- Communicate deprecation timeline clearly

## Pagination

### Cursor-Based Pagination (Recommended)
```http
GET /users?limit=25&after=cursor_abc123

Response:
{
  "data": [...],
  "pagination": {
    "has_more": true,
    "next_cursor": "cursor_def456"
  }
}
```

### Offset-Based Pagination
```http
GET /users?limit=25&offset=50

Response:
{
  "data": [...],
  "meta": {
    "total": 150,
    "page": 3,
    "per_page": 25,
    "total_pages": 6
  }
}
```

### Guidelines
- Default limit: 25 items
- Maximum limit: 100 items
- Always provide pagination metadata
- Use cursor-based pagination for large datasets
- Include `has_more` flag for cursor pagination

## Rate Limiting

### Headers
```http
X-RateLimit-Limit: 1000
X-RateLimit-Remaining: 999
X-RateLimit-Reset: 1640995200
```

### Response for Rate Limit Exceeded
```json
{
  "error": {
    "code": "rate_limit_exceeded",
    "message": "Rate limit exceeded. Try again in 60 seconds.",
    "retry_after": 60
  }
}
```

### Guidelines
- Implement rate limiting per API key
- Return rate limit headers with all responses
- Use `429 Too Many Requests` status code
- Provide clear retry guidance

## Documentation

### OpenAPI Specification
- Maintain OpenAPI 3.0 specification for all endpoints
- Include detailed descriptions and examples
- Document all request/response schemas
- Provide interactive API explorer

### Endpoint Documentation Requirements
- Clear endpoint description
- Request/response examples
- Error response examples
- Authentication requirements
- Rate limiting information

## Testing

### API Testing Requirements
- Unit tests for all business logic
- Integration tests for all endpoints
- Contract tests for API specification compliance
- Performance tests for critical endpoints
- Security tests for authentication and authorization

### Test Data Guidelines
- Use realistic test data
- Include edge cases and error scenarios
- Maintain separate test environments
- Never use production data in tests

## Examples

### Complete CRUD Example for Users Resource

#### Create User
```http
POST /v1/users
Content-Type: application/json
Authorization: Bearer your_api_key

{
  "user": {
    "first_name": "John",
    "last_name": "Doe",
    "email": "john.doe@example.com",
    "role": "member"
  }
}

201 Created
Location: /v1/users/user_123
{
  "data": {
    "id": "user_123",
    "type": "user",
    "attributes": {
      "first_name": "John",
      "last_name": "Doe",
      "email": "john.doe@example.com",
      "role": "member",
      "created_at": "2024-01-15T10:30:00Z",
      "updated_at": "2024-01-15T10:30:00Z"
    }
  }
}
```

#### Get User
```http
GET /v1/users/user_123
Authorization: Bearer your_api_key

200 OK
{
  "data": {
    "id": "user_123",
    "type": "user",
    "attributes": {
      "first_name": "John",
      "last_name": "Doe",
      "email": "john.doe@example.com",
      "role": "member",
      "created_at": "2024-01-15T10:30:00Z",
      "updated_at": "2024-01-15T10:30:00Z"
    }
  }
}
```

#### Update User
```http
PUT /v1/users/user_123
Content-Type: application/json
Authorization: Bearer your_api_key

{
  "user": {
    "first_name": "John",
    "last_name": "Smith",
    "email": "john.smith@example.com",
    "role": "admin"
  }
}

200 OK
{
  "data": {
    "id": "user_123",
    "type": "user",
    "attributes": {
      "first_name": "John",
      "last_name": "Smith",
      "email": "john.smith@example.com",
      "role": "admin",
      "created_at": "2024-01-15T10:30:00Z",
      "updated_at": "2024-01-15T14:20:00Z"
    }
  }
}
```

#### Delete User
```http
DELETE /v1/users/user_123
Authorization: Bearer your_api_key

204 No Content
```

---

*This style guide is a living document and should be updated as our API practices evolve. For questions or suggestions, please contact the API team.*