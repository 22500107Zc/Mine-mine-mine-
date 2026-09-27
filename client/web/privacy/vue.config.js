const buildVueConfig = require('./vue.config-builder')

module.exports = buildVueConfig({
  appFlavour: 'Data Privacy',
  appName: 'privacy',
  appLabel: 'CulpOS | Data Privacy',
  theme: 'corteza-base',
  packageAlias: 'corteza-webapp-privacy',
})
