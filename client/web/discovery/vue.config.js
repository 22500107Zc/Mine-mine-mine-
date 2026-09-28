const buildVueConfig = require('./vue.config-builder')

module.exports = buildVueConfig({
  appFlavour: 'Search',
  appName: 'discovery',
  appLabel: 'St.Cloud~OS | Search',
  theme: 'corteza-base',
  packageAlias: 'corteza-webapp-discovery',
})
