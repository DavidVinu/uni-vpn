package de.davidvinu.univpn.ui

import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ColumnScope
import androidx.compose.foundation.layout.Row
import androidx.compose.ui.Alignment
import androidx.compose.foundation.layout.RowScope
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.outlined.Info
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.ListItem
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TopAppBar
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp
import de.davidvinu.univpn.AppModel

/** Top bar of every screen but Home: back arrow and title, as in Android's Settings app. */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun Bar(model: AppModel, title: String, onBack: (() -> Unit)? = { model.back() }, actions: @Composable RowScope.() -> Unit = {}) {
    TopAppBar(
        title = { Text(title) },
        navigationIcon = {
            if (onBack != null) {
                IconButton(onClick = onBack) {
                    Icon(Icons.AutoMirrored.Filled.ArrowBack, contentDescription = model.t("app.back"))
                }
            }
        },
        actions = actions,
    )
}

/** A hint that would otherwise be text on the screen: behind an info icon (project rule). */
@Composable
fun Info(model: AppModel, text: String) {
    var open by remember { mutableStateOf(false) }
    IconButton(onClick = { open = true }) {
        Icon(Icons.Outlined.Info, contentDescription = model.t("app.help"))
    }
    if (open) {
        DialogShown(model)
        AlertDialog(
            // The second tap of a double tap on the icon lands beside the dialog.
            onDismissRequest = { if (!model.justChanged) open = false },
            confirmButton = { TextButton(onClick = { open = false }) { Text(model.t("app.done")) } },
            // Long help (a university's steps) at a large font size: the dialog does not scroll
            // by itself and would cut off the end.
            text = { Text(text, Modifier.verticalScroll(rememberScrollState())) },
        )
    }
}

/**
 * A dialog that opens or closes on a tap changes what is on screen, like a new screen: the second
 * tap of a double tap would land beside it, or on what was under its button (App.kt doubleTapGuard).
 */
@Composable
fun DialogShown(model: AppModel) {
    DisposableEffect(Unit) {
        model.screenChanged()
        onDispose { model.screenChanged() }
    }
}

@Composable
fun Section(title: String) {
    Text(
        title,
        style = MaterialTheme.typography.titleSmall,
        color = MaterialTheme.colorScheme.primary,
        modifier = Modifier.padding(start = 16.dp, top = 20.dp, bottom = 4.dp),
    )
}

/** A row of a settings list: title, the current value under it. */
@Composable
fun SettingRow(title: String, value: String? = null, onClick: (() -> Unit)? = null, trailing: @Composable (() -> Unit)? = null) {
    ListItem(
        headlineContent = { Text(title) },
        supportingContent = value?.takeIf { it.isNotEmpty() }?.let { { Text(it) } },
        trailingContent = trailing,
        modifier = if (onClick != null) Modifier.clickable(onClick = onClick) else Modifier,
    )
}

@Composable
fun ErrorText(text: String?, modifier: Modifier = Modifier) {
    if (!text.isNullOrEmpty()) {
        Text(text, color = MaterialTheme.colorScheme.error, style = MaterialTheme.typography.bodyMedium, modifier = modifier)
    }
}

@Composable
fun Actions(content: @Composable RowScope.() -> Unit) {
    Row(horizontalArrangement = Arrangement.spacedBy(8.dp, Alignment.End), modifier = Modifier.padding(top = 8.dp), content = content)
}

@Composable
fun Page(content: @Composable ColumnScope.() -> Unit) {
    Column(Modifier.padding(horizontal = 24.dp, vertical = 8.dp), verticalArrangement = Arrangement.spacedBy(12.dp), content = content)
}
