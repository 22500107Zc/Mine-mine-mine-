const buildVueConfig = require('./vue.config-builder')

module.exports = buildVueConfig({
  appFlavour: 'CulpOS',
  appName: 'one',
  appLabel: 'CulpOS',
  theme: 'corteza-base',
  packageAlias: 'corteza-webapp-one',
})
