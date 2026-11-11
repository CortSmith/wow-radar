import * as cdk from 'aws-cdk-lib';
import { Construct } from 'constructs';
import * as s3 from 'aws-cdk-lib/aws-s3';
import * as s3deploy from 'aws-cdk-lib/aws-s3-deployment';
import * as cf from 'aws-cdk-lib/aws-cloudfront';
import * as origins from 'aws-cdk-lib/aws-cloudfront-origins';
import * as lambda from 'aws-cdk-lib/aws-lambda';
import * as ddb from 'aws-cdk-lib/aws-dynamodb';
import * as logs from 'aws-cdk-lib/aws-logs';
import * as budgets from 'aws-cdk-lib/aws-budgets';
import * as path from 'path';

export interface RadarProps extends cdk.StackProps { budgetEmail?: string }

export class RadarStack extends cdk.Stack {
  constructor(scope: Construct, id: string, props: RadarProps) {
    super(scope, id, props);

    const table = new ddb.Table(this, 'Table', {
      partitionKey: { name: 'PK', type: ddb.AttributeType.STRING },
      sortKey: { name: 'SK', type: ddb.AttributeType.STRING },
      billingMode: ddb.BillingMode.PAY_PER_REQUEST,
      removalPolicy: cdk.RemovalPolicy.RETAIN,
    });

    table.addGlobalSecondaryIndex({
      indexName: 'zone-idx',
      partitionKey: { name: 'zk', type: ddb.AttributeType.STRING },
      sortKey: { name: 'ls', type: ddb.AttributeType.NUMBER },
      projectionType: ddb.ProjectionType.INCLUDE,
      nonKeyAttributes: ['name', 'reaction', 'level', 'maxHp', 'x', 'y', 'mapId', 'zone', 'firstSeen', 'lastSeen', 'posTime'],
    });

    const fn = new lambda.Function(this, 'Api', {
      runtime: lambda.Runtime.NODEJS_22_X,
      architecture: lambda.Architecture.ARM_64,
      memorySize: 256,
      timeout: cdk.Duration.seconds(25), // CloudFront origin timeout is 30s
      handler: 'index.handler',
      code: lambda.Code.fromAsset(path.join(__dirname, '..', 'lambda')),
      environment: { TABLE: table.tableName, MAX_ACCOUNTS_PER_KEY: '5' },
      logGroup: new logs.LogGroup(this, 'ApiLogs', { retention: logs.RetentionDays.ONE_WEEK, removalPolicy: cdk.RemovalPolicy.DESTROY }),
    });
    table.grantReadWriteData(fn);
    const url = fn.addFunctionUrl({ authType: lambda.FunctionUrlAuthType.NONE });

    const bucket = new s3.Bucket(this, 'Site', {
      blockPublicAccess: s3.BlockPublicAccess.BLOCK_ALL,
      removalPolicy: cdk.RemovalPolicy.DESTROY,
      autoDeleteObjects: true,
    });

    const apiOrigin = new origins.FunctionUrlOrigin(url);
    const dist = new cf.Distribution(this, 'Cdn', {
      priceClass: cf.PriceClass.PRICE_CLASS_100,
      defaultRootObject: 'index.html',
      defaultBehavior: {
        origin: origins.S3BucketOrigin.withOriginAccessControl(bucket),
        viewerProtocolPolicy: cf.ViewerProtocolPolicy.REDIRECT_TO_HTTPS,
        compress: true,
        cachePolicy: cf.CachePolicy.CACHING_OPTIMIZED,
      },
      additionalBehaviors: {
        '/api/*': {
          origin: apiOrigin,
          viewerProtocolPolicy: cf.ViewerProtocolPolicy.HTTPS_ONLY,
          allowedMethods: cf.AllowedMethods.ALLOW_ALL,
          cachePolicy: cf.CachePolicy.CACHING_DISABLED,
          // forwards Authorization header; Host is not forwarded (required for Function URLs)
          originRequestPolicy: cf.OriginRequestPolicy.ALL_VIEWER_EXCEPT_HOST_HEADER,
          compress: true,
        },
      },
    });

    new s3deploy.BucketDeployment(this, 'Deploy', {
      sources: [s3deploy.Source.asset(path.join(__dirname, '..', 'site'))],
      destinationBucket: bucket,
      distribution: dist,
      distributionPaths: ['/*'], // one wildcard invalidation per deploy (1,000 paths/mo free)
      memoryLimit: 512,
      prune: true,
    });

    if (props.budgetEmail) {
      new budgets.CfnBudget(this, 'Budget', {
        budget: { budgetType: 'COST', timeUnit: 'MONTHLY', budgetLimit: { amount: 5, unit: 'USD' } },
        notificationsWithSubscribers: [{
          notification: { notificationType: 'ACTUAL', comparisonOperator: 'GREATER_THAN', threshold: 80, thresholdType: 'PERCENTAGE' },
          subscribers: [{ subscriptionType: 'EMAIL', address: props.budgetEmail }],
        }],
      });
    }

    new cdk.CfnOutput(this, 'SiteUrl', { value: `https://${dist.distributionDomainName}` });
    new cdk.CfnOutput(this, 'TableName', { value: table.tableName });
  }
}
