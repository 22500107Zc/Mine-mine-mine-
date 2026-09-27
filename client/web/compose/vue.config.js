const buildVueConfig = require('./vue.config-builder')

module.exports = buildVueConfig({
  appFlavour: 'Workspace',
  appName: 'compose',
  appLabel: 'CulpOS | Workspace',
  theme: 'corteza-base',
  packageAlias: 'corteza-webapp-compose',
})
