const buildVueConfig = require('./vue.config-builder')

module.exports = buildVueConfig({
  appFlavour: 'Reports',
  appName: 'reporter',
  appLabel: 'St.Cloud~OS | Reports',
  theme: 'corteza-base',
  packageAlias: 'corteza-webapp-reporter',
})
