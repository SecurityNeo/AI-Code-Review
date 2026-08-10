#!/usr/bin/env python3
import boto3
from botocore.client import Config

s3 = boto3.client(
    's3',
    endpoint_url='http://10.210.20.50:9000',
    aws_access_key_id='admin',
    aws_secret_access_key='Passw0rd@_',
    config=Config(signature_version='s3v4'),
    region_name='us-east-1'
)

bucket = 'ai-code-review'

# Try listing with various prefixes
prefixes = [
    '',
    'codeguard/',
    'codeguard/tasks/',
    'codeguard/tasks/156/',
    'codeguard/snapshots/',
    'tasks/',
    'tasks/156/',
    'snapshots/',
    'pipeline/',
    'codeguard/pipeline/',
]

print("=== Listing objects with various prefixes ===")
for prefix in prefixes:
    print(f"\n--- Prefix: '{prefix}' ---")
    try:
        resp = s3.list_objects_v2(Bucket=bucket, Prefix=prefix, MaxKeys=1000)
        contents = resp.get('Contents', [])
        if not contents:
            print("  (no objects)")
        for obj in contents:
            print(f"  {obj['Key']} (size={obj['Size']}, modified={obj['LastModified']})")
    except Exception as e:
        print(f"  ERROR: {e}")

# Also do a full recursive list with pagination
print("\n=== Full recursive list ===")
all_keys = []
paginator = s3.get_paginator('list_objects_v2')
for page in paginator.paginate(Bucket=bucket):
    for obj in page.get('Contents', []):
        all_keys.append(obj)

print(f"Total objects: {len(all_keys)}")

# Filter for 156
keys_156 = [o for o in all_keys if '156' in o['Key']]
print(f"\nObjects containing '156': {len(keys_156)}")
for obj in keys_156:
    print(f"  {obj['Key']} (size={obj['Size']}, modified={obj['LastModified']})")

# Filter for context_extract
keys_ctx = [o for o in all_keys if 'context_extract' in o['Key']]
print(f"\nObjects containing 'context_extract': {len(keys_ctx)}")
for obj in keys_ctx:
    print(f"  {obj['Key']} (size={obj['Size']}, modified={obj['LastModified']})")

# Stat objects with 156
print("\n=== Stat objects containing '156' ===")
for obj in keys_156:
    key = obj['Key']
    try:
        head = s3.head_object(Bucket=bucket, Key=key)
        print(f"\nKey: {key}")
        print(f"  ContentType: {head.get('ContentType')}")
        print(f"  ContentLength: {head.get('ContentLength')}")
        print(f"  LastModified: {head.get('LastModified')}")
        print(f"  ETag: {head.get('ETag')}")
        print(f"  Metadata: {head.get('Metadata', {})}")
    except Exception as e:
        print(f"  ERROR stat {key}: {e}")

# Stat objects with context_extract
print("\n=== Stat objects containing 'context_extract' ===")
for obj in keys_ctx:
    key = obj['Key']
    try:
        head = s3.head_object(Bucket=bucket, Key=key)
        print(f"\nKey: {key}")
        print(f"  ContentType: {head.get('ContentType')}")
        print(f"  ContentLength: {head.get('ContentLength')}")
        print(f"  LastModified: {head.get('LastModified')}")
        print(f"  ETag: {head.get('ETag')}")
        print(f"  Metadata: {head.get('Metadata', {})}")
    except Exception as e:
        print(f"  ERROR stat {key}: {e}")
