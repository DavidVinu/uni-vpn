package de.davidvinu.univpn

import android.app.PendingIntent
import android.appwidget.AppWidgetManager
import android.appwidget.AppWidgetProvider
import android.content.ComponentName
import android.content.Context
import android.content.Intent
import android.widget.RemoteViews

/**
 * Home screen widget: one tap turns Uni VPN on or off. Role model: Android's own Quick Settings
 * tiles as 1x1 widgets (Pixel): the icon alone, filled with the accent colour while on.
 */
class VpnWidget : AppWidgetProvider() {
    override fun onUpdate(context: Context, manager: AppWidgetManager, ids: IntArray) =
        manager.updateAppWidget(ids, views(context))

    companion object {
        fun update(context: Context) {
            val manager = AppWidgetManager.getInstance(context) ?: return
            val ids = manager.getAppWidgetIds(ComponentName(context, VpnWidget::class.java))
            if (ids.isNotEmpty()) manager.updateAppWidget(ids, views(context))
        }

        private fun views(context: Context): RemoteViews {
            val layout = if (UniVpnService.isOn.value) R.layout.widget_on else R.layout.widget_off
            // An activity, not a broadcast: Android lets a tap on a widget start one, and from
            // there the VPN service may start (ToggleActivity).
            val tap = PendingIntent.getActivity(
                context, 0,
                Intent(context, ToggleActivity::class.java).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK),
                PendingIntent.FLAG_IMMUTABLE,
            )
            return RemoteViews(context.packageName, layout).apply {
                setOnClickPendingIntent(android.R.id.background, tap)
            }
        }
    }
}

/** The tile and the widget follow the VPN. */
object Toggles {
    fun update(context: Context) {
        VpnTileService.update(context)
        VpnWidget.update(context)
    }
}
