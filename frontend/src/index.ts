import { defineWebApplication, Extension, AppMenuItemExtension } from '@ownclouders/web-pkg'
import { urlJoin } from '@ownclouders/web-client'
import { RouteRecordRaw } from 'vue-router'
import { computed } from 'vue'
import { useGettext } from 'vue3-gettext'
import { setAppConfig } from './composables/useAppConfig'

export default defineWebApplication({
  setup(args) {
    const { $gettext } = useGettext()

    const appInfo = {
      id: 'workflows',
      name: $gettext('Workflows'),
      icon: 'flow-chart',
      color: '#2a6f97'
    }

    const rawConfig = (args.applicationConfig ?? {}) as Record<string, string>
    setAppConfig({ backendUrl: rawConfig.backendUrl ?? '' })

    const routes: RouteRecordRaw[] = [
      {
        path: '/',
        redirect: `/${appInfo.id}/workflows`
      },
      {
        path: '/workflows',
        name: 'workflows',
        component: () => import('./views/WorkflowList.vue'),
        meta: {
          authContext: 'user',
          title: $gettext('Workflows')
        }
      },
      {
        path: '/workflows/:id',
        name: 'workflow-builder',
        component: () => import('./views/WorkflowBuilder.vue'),
        props: true,
        meta: {
          authContext: 'user',
          title: $gettext('Workflow')
        }
      }
    ]

    const extensions = computed<Extension[]>(() => {
      const menuItems: AppMenuItemExtension[] = [
        {
          id: `app.${appInfo.id}.menuItem`,
          type: 'appMenuItem',
          label: () => appInfo.name,
          color: appInfo.color,
          icon: appInfo.icon,
          path: urlJoin(appInfo.id)
        }
      ]
      return [...menuItems]
    })

    return {
      appInfo,
      routes,
      extensions
    }
  }
})
