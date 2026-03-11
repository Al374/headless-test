# Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved. 
# SPDX-License-Identifier: MIT-0

from typing import Any
import boto3
import botocore
import os
from mcp.server.fastmcp import FastMCP
from mcp.server.fastmcp.utilities.logging import get_logger
import json

logger = get_logger(__name__)

config = botocore.config.Config(
    read_timeout=600,
    connect_timeout=600,
    retries={"max_attempts": 0}
)

# Initialize FastMCP server
mcp = FastMCP("api-helper")

@mcp.tool()
async def ask_api_expert(request: str) -> str:
    """ 
    An extensive API expert tool for asking questions and getting guidance related to the APIs and Amazon API Gateway service, API management and governance.
    This tool analyzes request, uses wide knowledge base with latest documentation of the services, best practices and implementation guidance, 
    blog posts, community content, technical support answers.
    
    Use this tool when you received a question, request, task, inquiry related to:
    - API development
    - API management
    - API lifecycle
    - API planning, development, testing
    - Security of the APIs and how to secure APIs
    - Deployment, publishing of the APIs, CI/CD pipelines for APIs
    - Scaling and operating APIs on AWS
    - Monitoring and observability of the APIs, including logging, metrics, tracing
    - API usage analytics, operational and business insights, monetization of the APIs
    - API discoverability, developer portals
    - API first design
    - API best practices
    - API governance, security controls, governance tools, preventative/proactive/detective/responsive controls for APIs
    - Amazon API Gateway service
    - REST implementation

    Args:
        request: 
            question for the API Expert 
    """
    
    # Initialize the Lambda client with the specified region and configuration
    lambda_client = boto3.client(
        'lambda',
        config=config
    )
    try:
        logger.debug(f"API Expert tool request: {request}")
        # Invoke Lambda function for API Expert agent specified in environment variable
        result = lambda_client.invoke(
            FunctionName=os.getenv("API_EXPERT_LAMBDA_NAME","api-expert-agent"),
            InvocationType='RequestResponse',
            Payload=bytes(json.dumps({"request": request}), 'utf-8')
        )

        logger.debug(f"API Expert agent response: {result}")
        return result
    except Exception as e:
        logger.error(f"Error invoking API Expert agent: {e}")
        return "Error: Could not complete requested action."


@mcp.tool()
async def inspect_API(request: str) -> str:
    """ 
    An API inspector tool retrieves and inspects API definition and configuration based on best practices and internal organizational requirements, style guides.
    This tool is designed to analyze and identify potential issues in API Gateway configurations. 

    Use this tool to inspect and evaluate API endpoints against AWS best practices, security standards, and Well-Architected principles. 
    It provides configuration review and improvement recommendations.
    
    The tool retrieves API configuration data directly from AWS accounts when provided with an API ID. 
    It examines critical aspects including:
     - security configurations, 
     - throttling settings,
     - resource limits, 
     - request validation,
     - WAF integration, 
     - observability setups, 
     - documentation completeness. 
     
     After analysis, tool delivers a structured assessment of identified issues alongside actionable recommendations for improvement.
    
    Args:
        request: 
            Request for an API inspection that includes ID of an API to be inspected 
    """

    # Initialize the Lambda client with the specified region and configuration
    lambda_client = boto3.client(
        'lambda',
        config=config
    )
    try:
        logger.debug(f"API Inspector tool request: {request}")
        # Invoke Lambda function for API Inspector agent specified in environment variable
        result = lambda_client.invoke(
            FunctionName=os.getenv("API_INSPECTOR_LAMBDA_NAME", "api-inspector-agent"),
            InvocationType='RequestResponse',
            Payload=bytes(json.dumps({"request": request}), 'utf-8')
        )

        logger.debug(f"API Inspector agent response: {result}")
        return result
    except Exception as e:
        logger.error(f"Error invoking API Inspector agent: {e}")
        return "Error: Could not complete requested action."

if __name__ == "__main__":
    # Initialize and run the server
    logger.info("Starting API Helper MCP server")
    mcp.run(transport='stdio')
