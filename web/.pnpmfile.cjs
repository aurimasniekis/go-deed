// TypeScript 7 (the native compiler) type-checks the app, but it ships no JavaScript compiler API. typescript-eslint,
// ts-api-utils and openapi-typescript need that API, so they get TypeScript 6 as a private dependency instead of
// resolving their `typescript` peer to the root TypeScript 7.
const needsJsApi = /^(@typescript-eslint\/|typescript-eslint$|ts-api-utils$|openapi-typescript$)/

module.exports = {
  hooks: {
    readPackage(pkg) {
      if (needsJsApi.test(pkg.name)) {
        if (pkg.peerDependencies && pkg.peerDependencies.typescript) delete pkg.peerDependencies.typescript
        if (pkg.peerDependenciesMeta && pkg.peerDependenciesMeta.typescript) delete pkg.peerDependenciesMeta.typescript
        pkg.dependencies = { ...pkg.dependencies, typescript: 'npm:typescript@6.0.3' }
      }
      return pkg
    },
  },
}
