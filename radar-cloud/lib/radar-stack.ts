/**
 * RadarStack: the whole cloud footprint of radar-cloud, built for near-zero idle cost
 * (everything is pay-per-request; no VPC, NAT, load balancer, WAF, KMS key or API Gateway).
 * Construct ids follow NAMING.md: ContactsTable, RelayFunction, HullBucket, Skyline, FuelGauge.
 */
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

// ---------------------------------------------------------------- tunables
/** Beacon (API key) limit; must match the lambda's MAX_ACCOUNTS_PER_KEY environment variable. */
const BEACON_MAX_OPERATORS = 5;
const RELAY_MEMORY_MB = 256;
/** CloudFront gives the origin 30 s; time out just before it so the client gets our error, not CloudFront's. */
const RELAY_TIMEOUT_SEC = 25;
const SITE_DEPLOY_MEMORY_MB = 512;
const FUEL_GAUGE_LIMIT_USD = 5;
const FUEL_GAUGE_ALERT_PERCENT = 80;
const API_PATH_PATTERN = '/api/*';
/** Attributes copied into the zone-idx GSI; counters are deliberately left out to keep index writes small. */
const ZONE_INDEX_PROJECTION = [
  'name',
  'reaction',
  'level',
  'maxHp',
  'x',
  'y',
  'mapId',
  'zone',
  'firstSeen',
  'lastSeen',
  'posTime',
];

export interface RadarProps extends cdk.StackProps {
  /** When set, a monthly cost budget alert is created and sent to this address. */
  budgetEmail?: string;
}

export class RadarStack extends cdk.Stack {
  constructor(scope: Construct, id: string, props: RadarProps) {
    super(scope, id, props);

    const table = this.createContactsTable();
    const relay = this.createRelayFunction(table);
    const bucket = this.createHullBucket();
    const skyline = this.createSkyline(bucket, relay);
    this.deploySite(bucket, skyline);
    if (props.budgetEmail) this.createFuelGauge(props.budgetEmail);

    cdk.Tags.of(table).add('layer', 'data');
    cdk.Tags.of(relay).add('layer', 'api');
    cdk.Tags.of(bucket).add('layer', 'site');
    cdk.Tags.of(skyline).add('layer', 'cdn');
    cdk.Tags.of(this).add('app', 'radar');

    // Output ids are a frozen contract with the deploy scripts.
    new cdk.CfnOutput(this, 'SiteUrl', { value: `https://${skyline.distributionDomainName}` });
    new cdk.CfnOutput(this, 'TableName', { value: table.tableName });
  }

  /**
   * Cost: on-demand billing (no idle cost, no capacity planning), no point-in-time recovery (extra per-GB cost),
   * RETAIN so a stack delete can never destroy contact data. One sparse GSI (zone-idx) serves zone sweeps;
   * INCLUDE projection instead of ALL because every projected attribute is paid for again on each index write.
   */
  private createContactsTable(): ddb.Table {
    const table = new ddb.Table(this, 'ContactsTable', {
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
      nonKeyAttributes: ZONE_INDEX_PROJECTION,
    });
    return table;
  }

  /**
   * Cost: arm64 (cheaper per ms), small memory, Function URL instead of API Gateway (no per-request fee),
   * and one-week log retention so CloudWatch storage cannot grow unbounded.
   * The Function URL is public by design: authentication is the lambda's own signed pass.
   */
  private createRelayFunction(table: ddb.Table): lambda.FunctionUrl {
    const relay = new lambda.Function(this, 'RelayFunction', {
      runtime: lambda.Runtime.NODEJS_22_X,
      architecture: lambda.Architecture.ARM_64,
      memorySize: RELAY_MEMORY_MB,
      timeout: cdk.Duration.seconds(RELAY_TIMEOUT_SEC),
      handler: 'index.handler',
      code: lambda.Code.fromAsset(path.join(__dirname, '..', 'lambda')),
      environment: { TABLE: table.tableName, MAX_ACCOUNTS_PER_KEY: String(BEACON_MAX_OPERATORS) },
      logGroup: new logs.LogGroup(this, 'RelayLogs', {
        retention: logs.RetentionDays.ONE_WEEK,
        removalPolicy: cdk.RemovalPolicy.DESTROY,
      }),
    });
    table.grantReadWriteData(relay); // least privilege: item-level actions on this table and its indexes only
    return relay.addFunctionUrl({ authType: lambda.FunctionUrlAuthType.NONE });
  }

  /** Cost: private bucket, auto-emptied on destroy (site files are rebuilt from the repo on every deploy). */
  private createHullBucket(): s3.Bucket {
    return new s3.Bucket(this, 'HullBucket', {
      blockPublicAccess: s3.BlockPublicAccess.BLOCK_ALL,
      removalPolicy: cdk.RemovalPolicy.DESTROY,
      autoDeleteObjects: true,
    });
  }

  /**
   * Cost: PRICE_CLASS_100 (cheapest edge locations). Static files are cached; /api/* is never cached and
   * forwards every viewer header except Host (Function URLs reject a foreign Host) so Authorization arrives.
   */
  private createSkyline(bucket: s3.Bucket, relayUrl: lambda.FunctionUrl): cf.Distribution {
    return new cf.Distribution(this, 'Skyline', {
      priceClass: cf.PriceClass.PRICE_CLASS_100,
      defaultRootObject: 'index.html',
      defaultBehavior: {
        origin: origins.S3BucketOrigin.withOriginAccessControl(bucket),
        viewerProtocolPolicy: cf.ViewerProtocolPolicy.REDIRECT_TO_HTTPS,
        compress: true,
        cachePolicy: cf.CachePolicy.CACHING_OPTIMIZED,
      },
      additionalBehaviors: {
        [API_PATH_PATTERN]: {
          origin: new origins.FunctionUrlOrigin(relayUrl),
          viewerProtocolPolicy: cf.ViewerProtocolPolicy.HTTPS_ONLY,
          allowedMethods: cf.AllowedMethods.ALLOW_ALL,
          cachePolicy: cf.CachePolicy.CACHING_DISABLED,
          originRequestPolicy: cf.OriginRequestPolicy.ALL_VIEWER_EXCEPT_HOST_HEADER,
          compress: true,
        },
      },
    });
  }

  /** Cost: a single wildcard invalidation per deploy (the first 1,000 paths per month are free). */
  private deploySite(bucket: s3.Bucket, skyline: cf.Distribution): void {
    new s3deploy.BucketDeployment(this, 'SiteDeploy', {
      sources: [s3deploy.Source.asset(path.join(__dirname, '..', 'site'))],
      destinationBucket: bucket,
      distribution: skyline,
      distributionPaths: ['/*'],
      memoryLimit: SITE_DEPLOY_MEMORY_MB,
      prune: true,
    });
  }

  /** Cost: the first two budgets per account are free; the alert fires on actual spend, not forecast. */
  private createFuelGauge(email: string): void {
    new budgets.CfnBudget(this, 'FuelGauge', {
      budget: { budgetType: 'COST', timeUnit: 'MONTHLY', budgetLimit: { amount: FUEL_GAUGE_LIMIT_USD, unit: 'USD' } },
      notificationsWithSubscribers: [
        {
          notification: {
            notificationType: 'ACTUAL',
            comparisonOperator: 'GREATER_THAN',
            threshold: FUEL_GAUGE_ALERT_PERCENT,
            thresholdType: 'PERCENTAGE',
          },
          subscribers: [{ subscriptionType: 'EMAIL', address: email }],
        },
      ],
    });
  }
}
