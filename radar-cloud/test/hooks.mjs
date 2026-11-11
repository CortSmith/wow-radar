/**
 * Node module-loader hook: redirects the AWS SDK imports of the lambda (by exact specifier) to the in-memory
 * stubs, so the real lambda code runs in tests and in the local dev server without AWS.
 */
export async function resolve(spec, ctx, next) {
  if (spec === '@aws-sdk/lib-dynamodb')
    return { url: new URL('./stub-lib.mjs', import.meta.url).href, shortCircuit: true };
  if (spec === '@aws-sdk/client-dynamodb')
    return { url: new URL('./stub-client.mjs', import.meta.url).href, shortCircuit: true };
  return next(spec, ctx);
}
