export async function resolve(spec, ctx, next) {
  if (spec === '@aws-sdk/lib-dynamodb') return { url: new URL('./stub-lib.mjs', import.meta.url).href, shortCircuit: true };
  if (spec === '@aws-sdk/client-dynamodb') return { url: new URL('./stub-client.mjs', import.meta.url).href, shortCircuit: true };
  return next(spec, ctx);
}
