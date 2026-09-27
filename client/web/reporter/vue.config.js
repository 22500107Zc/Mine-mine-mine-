const buildVueConfig = require('./vue.config-builder')

module.exports = buildVueConfig({
  appFlavour: 'Reports',
  appName: 'reporter',
  appLabel: 'CulpOS | Reports',
  theme: 'corteza-base',
  packageAlias: 'corteza-webapp-reporter',
})
