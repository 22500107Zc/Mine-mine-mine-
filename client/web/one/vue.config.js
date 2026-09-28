const buildVueConfig = require('./vue.config-builder')

module.exports = buildVueConfig({
  appFlavour: 'St.Cloud~OS',
  appName: 'one',
  appLabel: 'St.Cloud~OS',
  theme: 'corteza-base',
  packageAlias: 'corteza-webapp-one',
})
