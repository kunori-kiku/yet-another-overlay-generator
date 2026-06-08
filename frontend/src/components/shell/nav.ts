import type { ComponentType, SVGProps } from 'react';
import { STRINGS } from '../../i18n';
import {
  OverviewIcon,
  DesignIcon,
  FleetIcon,
  DeployIcon,
  SecurityIcon,
  SettingsIcon,
} from './icons';

// Single source of truth for the sidebar taxonomy, shared by the Sidebar (and,
// from P2 onward, the Topbar breadcrumb). P2 augments each item with a `path`
// when sections become real routes; P4 adds per-mode visibility.
export interface NavItem {
  key: string;
  label: readonly [string, string];
  Icon: ComponentType<SVGProps<SVGSVGElement>>;
}

export const NAV_ITEMS: readonly NavItem[] = [
  { key: 'overview', label: STRINGS.navOverview, Icon: OverviewIcon },
  { key: 'design', label: STRINGS.navDesign, Icon: DesignIcon },
  { key: 'fleet', label: STRINGS.navFleet, Icon: FleetIcon },
  { key: 'deploy', label: STRINGS.navDeploy, Icon: DeployIcon },
  { key: 'security', label: STRINGS.navSecurity, Icon: SecurityIcon },
  { key: 'settings', label: STRINGS.navSettings, Icon: SettingsIcon },
];
