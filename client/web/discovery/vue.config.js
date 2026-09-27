const buildVueConfig = require('./vue.config-builder')

module.exports = buildVueConfig({
  appFlavour: 'Search',
  appName: 'discovery',
  appLabel: 'CulpOS | Search',
  theme: 'corteza-base',
  packageAlias: 'corteza-webapp-discovery',
})
