var FFZ = window.FrankerFaceZ,
	utils = require('../utils'),
	constants = require('../constants'),

	createElement = utils.createElement;


// ---------------
// Settings
// ---------------

FFZ.settings_info.conv_focus_on_click = {
	type: "boolean",
	value: false,
	no_mobile: true,
	visible: false,

	category: "Whispers",
	name: "Focus Input on Click",
	help: "Focus on a conversation's input box when you click it."
	};


FFZ.settings_info.top_conversations = {
	type: "boolean",
	value: false,
	no_mobile: true,

	category: "Whispers",
	name: "Position on Top",
	help: "Display the whisper UI at the top of the window instead of the bottom.",
	on_update: function(val) {
			document.body.classList.toggle('ffz-top-conversations', val);
		}
	};


FFZ.settings_info.hide_whispers_in_embedded_chat = {
	type: "boolean",
	value: false,
	no_bttv: 6,

	category: "Whispers",
	name: "Hide Whispers in Embedded Chat",
	help: "Do not display whispers on the dashboard, in pop-out chat, or in chat embedded into other websites.",

	on_update: function(val) {
		if ( ! val || this.has_bttv_6 )
			return;

		for(var room_id in this.rooms) {
			var room = this.rooms[room_id].room;
			if ( ! room )
				continue;

			var messages = room.get('messages'),
				length = messages && messages.length || 0,
				i = length;

			while(--i >= 0) {
				if ( messages[i] && messages[i].style === 'whisper' )
					messages.removeAt(i);
			}
		}
	}
};


FFZ.settings_info.hide_conversations_in_theatre = {
	type: "boolean",
	value: false,
	no_mobile: true,

	category: "Whispers",
	name: "Hide Whispers in Theater Mode",
	help: "Hide the whisper UI when the page is in theater mode.",
	on_update: function(val) {
			document.body.classList.toggle('ffz-theatre-conversations', val);
		}
	};


FFZ.settings_info.minimize_conversations = {
	type: "boolean",
	value: false,
	no_mobile: true,

	category: "Whispers",
	name: "Minimize Whisper UI",
	help: "Slide the whisper UI mostly out of view when it's not being used and you have no unread messages.",
	on_update: function(val) {
			document.body.classList.toggle('ffz-minimize-conversations', val);
		}
	};


// ---------------
// Initialization
// ---------------

FFZ.prototype.setup_conversations = function() {
	document.body.classList.toggle('ffz-top-conversations', this.settings.top_conversations);
	document.body.classList.toggle('ffz-minimize-conversations', this.settings.minimize_conversations);
	document.body.classList.toggle('ffz-theatre-conversations', this.settings.hide_conversations_in_theatre);

	this.update_views('component:twitch-conversations/conversation-window', this.modify_conversation_window);
	this.update_views('component:twitch-conversations/conversation-settings-menu', this.modify_conversation_menu);
	this.update_views('component:twitch-conversations/conversation-line', this.modify_conversation_line);
}


FFZ.prototype.modify_conversation_menu = function(component) {
	var f = this;
	utils.ember_reopen_view(component, {
		ffz_init: function() {
			var user = this.get('thread.otherUsername'),
				el = this.get('element'),
				sections = el && el.querySelectorAll('.options-section');

			if ( ! user || ! user.length || f.has_bttv_6 )
				return;

			if ( sections && sections.length )
				el.appendChild(createElement('div', 'options-divider'));

			var ffz_options = createElement('div', 'options-section'),
				card_link = createElement('a', 'ffz-show-card', "Open Moderation Card");

			card_link.addEventListener('click', function(e) {
				el.parentElement.classList.add('hidden');
				FFZ.chat_commands.card.call(f, null, [user]);
			});

			ffz_options.appendChild(card_link);
			el.appendChild(ffz_options);
		}
	})
}


FFZ.prototype.modify_conversation_window = function(component) {
	var f = this,
		Layout = utils.ember_lookup('service:layout');

	utils.ember_reopen_view(component, {
		headerBadges: Ember.computed("thread.participants", "currentUsername", function() {
			return [];
		}),

		ffzHeaderBadges: Ember.computed("thread.participants", "currentUsername", function() {
			var e = this.get("otherUser");
			return f.get_badges(e.get('username'), null, f.get_twitch_badges(e.get('badges')), null);
		}),

		ffzReplaceBadges: function() {
			var el = this.get('element'),
				badge_el = el && el.querySelector('.badges'),
				badges = this.get('ffzHeaderBadges');

			if ( ! el )
				return;

			if ( ! badge_el ) {
				badge_el = createElement('span', 'badges');
				var header = el && el.querySelector('.convoHeader .username');
				if ( ! header )
					return;

				header.insertBefore(badge_el, header.firstChild);
			}

			badge_el.innerHTML = f.render_badges(badges);
		}.observes('ffzHeaderBadges'),

		ffz_init: function() {
			var el = this.get('element'),
				header = el && el.querySelector('.convoHeader'),
				header_name = header && header.querySelector('.username'),

				raw_color = this.get('otherUser.color'),
				colors = raw_color && f._handle_color(raw_color),

				is_dark = (Layout && Layout.get('isTheatreMode')) || f.settings.dark_twitch;

			this.ffzReplaceBadges();

			if ( header_name && raw_color ) {
				header_name.style.color = (is_dark ? colors[1] : colors[0]);
				header_name.classList.add('has-color');
				header_name.setAttribute('data-color', raw_color);
			}

			jQuery('.badge', el).tipsy({gravity: utils.tooltip_placement(constants.TOOLTIP_DISTANCE, 'n')});
		}
	});
}


FFZ.prototype.modify_conversation_line = function(component) {
	var f = this,
		Layout = utils.ember_lookup('service:layout');

	utils.ember_reopen_view(component, {
		tokenizedMessage: function() {
			try {
				return f.tokenize_conversation_line(this.get('message'));
			} catch(err) {
				f.error("convo-line tokenizedMessage: " + err);
				return this._super();
			}

		}.property("message", "currentUsername"),

		click: function(e) {
			if ( e.target && e.target.classList.contains('deleted-link') )
				return f._deleted_link_click.call(e.target, e);

			if ( f._click_emote(e.target, e) )
				return;

			return this._super(e);
		},

		didUpdate: function() { this.ffzRender() },
		ffz_init: function() { this.ffzRender() },

		ffzRender: function() {
			var el = this.get('element'),
				e = [],

				username = this.get('message.from.username').toLowerCase(),
				raw_display = this.get('message.from.displayName'),
				alias = f.aliases[username],

				raw_color = this.get('message.from.color'),

				is_dark = (Layout && Layout.get('isTheatreMode')) || f.settings.dark_twitch,
				colors = raw_color && f._handle_color(raw_color),

				style = colors ? 'color:' + (is_dark ? colors[1] : colors[0]) : '',
				colored = colors ? ' has-color' : '',

				results = f.format_display_name(raw_display, username),

				myself = f.get_user(),
				from_me = myself && myself.login === username;


			e.push('<span class="from' +
						(alias ? ' ffz-alias' : '') +
						(results[1] ? ' html-tooltip' : '') +
						colored +
					'" style="' + style + '"' +
					(colors ? ' data-color="' + raw_color + '"' : '') +
					(results[1] ? ' title="' + utils.quote_attr(results[1]) + '"' : '') +
				'>' + results[0] + '</span>');

			e.push('<span class="colon">:</span> ');

			if ( ! this.get('isActionMessage') ) {
				style = '';
				colored = '';
			}

			e.push('<span class="message' + colored + '" style="' + style + (colors ? '" data-color="' + raw_color : '') + '">');
			e.push(f.render_tokens(this.get('tokenizedMessage'), true, f.settings.filter_whispered_links && !from_me));
			e.push('</span>');
			el.innerHTML = e.join('');
		}
	});
}