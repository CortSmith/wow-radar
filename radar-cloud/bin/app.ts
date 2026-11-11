/** CDK app entry: instantiates the RadarStation stack. Deploy with `cdk deploy -c budgetEmail=you@example.com`. */
import * as cdk from 'aws-cdk-lib';
import { RadarStack } from '../lib/radar-stack';

const app = new cdk.App();
new RadarStack(app, 'RadarStation', {
  env: { account: process.env.CDK_DEFAULT_ACCOUNT, region: process.env.CDK_DEFAULT_REGION },
  budgetEmail: app.node.tryGetContext('budgetEmail'),
});
