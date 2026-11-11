import { test } from 'node:test';
import assert from 'node:assert/strict';
import * as cdk from 'aws-cdk-lib';
import { Template, Match } from 'aws-cdk-lib/assertions';
import { RadarStack } from '../lib/radar-stack';

const mk = (budgetEmail?: string) => {
  const app = new cdk.App();
  const stack = new RadarStack(app, 'T', { env: { account: '111111111111', region: 'us-east-1' }, budgetEmail });
  return Template.fromStack(stack);
};

test('DynamoDB is on-demand, PK/SK, retained, no PITR', () => {
  const t = mk();
  t.hasResourceProperties('AWS::DynamoDB::Table', {
    BillingMode: 'PAY_PER_REQUEST',
    KeySchema: [{ AttributeName: 'PK', KeyType: 'HASH' }, { AttributeName: 'SK', KeyType: 'RANGE' }],
  });
  const tables = t.findResources('AWS::DynamoDB::Table');
  for (const r of Object.values(tables) as any[]) {
    assert.equal(r.DeletionPolicy, 'Retain');
    assert.ok(!r.Properties.PointInTimeRecoverySpecification?.PointInTimeRecoveryEnabled);
  }
});

test('API lambda: arm64, node22, 256MB, env, function URL, no API Gateway', () => {
  const t = mk();
  t.hasResourceProperties('AWS::Lambda::Function', Match.objectLike({
    Architectures: ['arm64'], Runtime: 'nodejs22.x', MemorySize: 256, Handler: 'index.handler',
    Environment: { Variables: Match.objectLike({ MAX_ACCOUNTS_PER_KEY: '5' }) },
  }));
  t.resourceCountIs('AWS::Lambda::Url', 1);
  t.resourceCountIs('AWS::ApiGatewayV2::Api', 0);
  t.resourceCountIs('AWS::ApiGateway::RestApi', 0);
});

test('Lambda can read/write the table but has no wildcard resources or admin actions', () => {
  const t = mk();
  const pol = JSON.stringify(t.findResources('AWS::IAM::Policy'));
  assert.match(pol, /dynamodb:BatchGetItem/);
  assert.match(pol, /dynamodb:BatchWriteItem/);
  assert.match(pol, /dynamodb:Query/);
  assert.doesNotMatch(pol, /dynamodb:\*/);
  assert.doesNotMatch(pol, /"Action":"\*"/);
});

test('S3 bucket is private; CloudFront uses OAC, HTTPS redirect, price class 100', () => {
  const t = mk();
  t.hasResourceProperties('AWS::S3::Bucket', {
    PublicAccessBlockConfiguration: { BlockPublicAcls: true, BlockPublicPolicy: true, IgnorePublicAcls: true, RestrictPublicBuckets: true },
  });
  t.resourceCountIs('AWS::CloudFront::OriginAccessControl', 1);
  t.hasResourceProperties('AWS::CloudFront::Distribution', {
    DistributionConfig: Match.objectLike({
      PriceClass: 'PriceClass_100', DefaultRootObject: 'index.html',
      DefaultCacheBehavior: Match.objectLike({ ViewerProtocolPolicy: 'redirect-to-https' }),
      CacheBehaviors: [Match.objectLike({ PathPattern: '/api/*', ViewerProtocolPolicy: 'https-only' })],
    }),
  });
});

test('/api/* is never cached and forwards all methods', () => {
  const t = mk();
  const d: any = Object.values(t.findResources('AWS::CloudFront::Distribution'))[0];
  const b = d.Properties.DistributionConfig.CacheBehaviors[0];
  assert.equal(b.AllowedMethods.length, 7);
  const policies = t.findResources('AWS::CloudFront::CachePolicy');
  // managed CACHING_DISABLED is referenced by id (4135ea2d-...)
  assert.equal(b.CachePolicyId, '4135ea2d-6df8-44a3-9df3-4b5a84be39ad');
  assert.equal(Object.keys(policies).length, 0);
});

test('Log retention is capped (no unbounded CloudWatch cost)', () => {
  const t = mk();
  const s = JSON.stringify(t.toJSON());
  assert.match(s, /"RetentionInDays":7/);
});

test('Budget exists only when email is provided', () => {
  assert.equal(Object.keys(mk().findResources('AWS::Budgets::Budget')).length, 0);
  const t = mk('me@example.com');
  t.hasResourceProperties('AWS::Budgets::Budget', Match.objectLike({ Budget: Match.objectLike({ BudgetLimit: { Amount: 5, Unit: 'USD' } }) }));
});

test('Cost guard: no NAT, VPC, WAF, Route53, Secrets Manager, KMS keys, API GW', () => {
  const s = JSON.stringify(mk().toJSON());
  for (const bad of ['AWS::EC2::NatGateway', 'AWS::EC2::VPC', 'AWS::WAFv2', 'AWS::Route53', 'AWS::SecretsManager', 'AWS::KMS::Key', 'AWS::ElasticLoadBalancingV2', 'AWS::RDS'])
    assert.ok(!s.includes(bad), bad);
});

test('Outputs present', () => {
  const t = mk();
  t.hasOutput('SiteUrl', {}); t.hasOutput('TableName', {});
});

test('zone-idx GSI: zk/ls keys, INCLUDE projection (not ALL), exactly one GSI', () => {
  const t = mk();
  const tbl: any = Object.values(t.findResources('AWS::DynamoDB::Table'))[0];
  const gsis = tbl.Properties.GlobalSecondaryIndexes;
  assert.equal(gsis.length, 1);
  const g = gsis[0];
  assert.equal(g.IndexName, 'zone-idx');
  assert.deepEqual(g.KeySchema, [{ AttributeName: 'zk', KeyType: 'HASH' }, { AttributeName: 'ls', KeyType: 'RANGE' }]);
  assert.equal(g.Projection.ProjectionType, 'INCLUDE');
  assert.ok(g.Projection.NonKeyAttributes.includes('lastSeen') && g.Projection.NonKeyAttributes.includes('x'));
  const defs = tbl.Properties.AttributeDefinitions;
  assert.deepEqual(defs.find((d: any) => d.AttributeName === 'ls').AttributeType, 'N');
});

test('Lambda IAM covers the GSI (index/*) so zone polls can Query it', () => {
  const pol = JSON.stringify(mk().findResources('AWS::IAM::Policy'));
  assert.match(pol, /\/index\/\*/);
});

test('Deploy invalidates the whole site so new/renamed scripts are never stale', () => {
  const t = mk();
  const s = JSON.stringify(t.toJSON());
  assert.match(s, /"DistributionPaths":\["\/\*"\]/);
});
