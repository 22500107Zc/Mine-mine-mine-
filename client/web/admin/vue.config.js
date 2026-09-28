const buildVueConfig = require('./vue.config-builder')

module.exports = buildVueConfig({
  appFlavour: 'Configuration Studio',
  appName: 'admin',
  appLabel: 'St.Cloud~OS | Configuration Studio',
  theme: 'corteza-base',
  packageAlias: 'corteza-webapp-admin',
})
